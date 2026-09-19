"""Explicitly experimental trusted-LAN scalar smoke test, NOT model sharding."""

import ipaddress
import json
import os
import threading
import importlib

from .runtime import config_from_file, verify_dependencies
from .security import Rejected, absolute_local, digest, integer, read_regular

RFC1918 = tuple(ipaddress.ip_network(value) for value in (
    "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
))


def validate_hostfile(path: str, expected_digest: str, rank: int) -> list:
    data = read_regular(absolute_local(path), 64 * 1024, private=True)
    if digest(data) != expected_digest:
        raise Rejected("ring hostfile changed")
    try:
        hosts = json.loads(data)
    except (ValueError, UnicodeError) as error:
        raise Rejected("invalid ring hostfile") from error
    if not isinstance(hosts, list) or not 2 <= len(hosts) <= 8:
        raise Rejected("experimental ring requires 2–8 explicitly approved LAN peers")
    integer(rank, 0, len(hosts) - 1)
    seen = set()
    for entry in hosts:
        if not isinstance(entry, list) or len(entry) != 1 or not isinstance(entry[0], str):
            raise Rejected("one explicit numeric IPv4 endpoint per rank required")
        address, separator, port = entry[0].rpartition(":")
        try:
            ip = ipaddress.IPv4Address(address)
            number = int(port)
        except ValueError as error:
            raise Rejected("invalid peer address") from error
        if not separator or not any(ip in network for network in RFC1918):
            raise Rejected("only explicit RFC1918 LAN addresses are approved")
        if not port.isascii() or not port.isdecimal() or not 1024 <= number <= 65535:
            raise Rejected("nonprivileged numeric peer port required")
        if address in seen:
            raise Rejected("each rank must use a distinct approved peer address")
        seen.add(address)
    return hosts


def smoke(config_path: str, hostfile: str, hostfile_sha256: str, rank: int,
          *, experimental_trusted_lan: bool = False) -> dict:
    if experimental_trusted_lan is not True:
        raise Rejected("explicit experimental trusted-LAN opt-in is required")
    hosts = validate_hostfile(hostfile, hostfile_sha256, rank)
    verify_dependencies(config_from_file(config_path))
    # Avoid Python signal handlers depending on a blocked C extension returning.
    # This fixed smoke process self-terminates; the Go owner should also use
    # a process-group deadline and cancel every rank on any failure.
    finished = threading.Event()

    def deadline():
        if not finished.wait(30):
            os._exit(124)

    threading.Thread(target=deadline, daemon=True).start()
    try:
        os.environ["MLX_RANK"] = str(rank)
        os.environ["MLX_HOSTFILE"] = str(absolute_local(hostfile))
        mx = importlib.import_module("mlx.core")
        world = mx.distributed.init(strict=True, backend="ring")
        if world.size() != len(hosts) or world.rank() != rank:
            raise Rejected("ring rank membership mismatch")
        value = mx.distributed.all_sum(mx.array([rank + 1], dtype=mx.int32), group=world)
        mx.eval(value)
        observed = int(value.item())
        expected = len(hosts) * (len(hosts) + 1) // 2
        if observed != expected:
            raise Rejected("ring collective returned an unexpected result")
        return {"schema_version": 1, "experimental": True, "backend": "ring",
                "rank": rank, "world_size": len(hosts), "sum": observed,
                "distributed_inference_enabled": False}
    finally:
        finished.set()
