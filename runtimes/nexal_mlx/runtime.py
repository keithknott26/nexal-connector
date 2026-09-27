"""The only Python execution path: approved local, single-host MLX inference."""

from __future__ import annotations

from contextlib import redirect_stdout
from importlib import metadata
import importlib
import os
from pathlib import Path
import platform
import re
import sys
import time

from .profiles import TOKENIZERS, encode_prompt, format_prompt

from .security import (
    Rejected, absolute_local, digest, exact_keys, integer, read_regular,
    strict_json, verify_model,
)

RUNTIME_VERSION = "0.2.0"
CANDIDATE_PINS = {"mlx": "0.29.3", "mlx-lm": "0.28.4", "transformers": "4.57.6"}


def probe(*, load_backend: bool = False) -> dict:
    """Metadata-only by default; importing this module never imports MLX."""
    result = {
        "schema_version": 1, "runtime_version": RUNTIME_VERSION,
        "system": platform.system(), "machine": platform.machine(),
        "python": platform.python_version(), "packages": {},
        "metal_available": None, "backend_imported": False,
        "inference_release_gate": "requires-reviewed-macos-lock-and-model",
        "distributed_execution": "disabled",
        "model_type_allowlist": sorted(TOKENIZERS),
    }
    for package in CANDIDATE_PINS:
        try:
            result["packages"][package] = metadata.version(package)
        except metadata.PackageNotFoundError:
            result["packages"][package] = None
    if load_backend and result["system"] == "Darwin" and result["machine"] == "arm64":
        try:
            mx = importlib.import_module("mlx.core")
            result["metal_available"] = bool(mx.metal.is_available())
            result["backend_imported"] = True
        except (ImportError, OSError, AttributeError):
            result["metal_available"] = False
    return result


def config_from_file(path: str) -> dict:
    config = strict_json(read_regular(absolute_local(path), 64 * 1024, private=True))
    exact_keys(config, {"schema_version", "model_directory", "model_manifest_sha256",
                        "dependency_receipt", "dependency_receipt_sha256"})
    if config["schema_version"] != 1:
        raise Rejected("unsupported runtime config")
    return config


def canonical_name(name: str) -> str:
    return re.sub(r"[-_.]+", "-", name).lower()


def verify_dependencies(config: dict) -> None:
    """Fail closed until a complete Mac-tested environment receipt is reviewed."""
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        raise Rejected("inference requires an Apple-silicon Mac")
    path = absolute_local(config["dependency_receipt"])
    data = read_regular(path, 1024 * 1024, private=True)
    if digest(data) != config["dependency_receipt_sha256"]:
        raise Rejected("dependency receipt hash mismatch")
    receipt = strict_json(data)
    exact_keys(receipt, {"schema_version", "macos_hardware_smoke_passed", "python_version",
                         "packages", "requirements_lock_sha256", "wheel_sha256"})
    if receipt["schema_version"] != 1 or receipt["macos_hardware_smoke_passed"] is not True:
        raise Rejected("Mac hardware release gate has not been approved")
    if receipt["python_version"] != platform.python_version():
        raise Rejected("Python version differs from the tested environment")
    packages = receipt["packages"]
    if not isinstance(packages, dict) or any(
        not isinstance(name, str) or name != canonical_name(name) or not isinstance(version, str)
        for name, version in packages.items()
    ):
        raise Rejected("receipt requires a complete normalized distribution inventory")
    observed = {}
    for distribution in metadata.distributions():
        name = canonical_name(distribution.metadata["Name"])
        if name in observed:
            raise Rejected("duplicate installed distributions are not approved")
        observed[name] = distribution.version
    if observed != packages or any(packages.get(name) != version for name, version in CANDIDATE_PINS.items()):
        raise Rejected("installed distributions differ from the reviewed complete lock")
    if not isinstance(receipt["requirements_lock_sha256"], str) or not re.fullmatch(
        r"[0-9a-f]{64}", receipt["requirements_lock_sha256"]
    ):
        raise Rejected("hashed requirements lock required")
    wheels = receipt["wheel_sha256"]
    if not isinstance(wheels, dict) or set(wheels) != set(packages) or any(
        not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{64}", value)
        for value in wheels.values()
    ):
        raise Rejected("one reviewed wheel hash per installed distribution required")


def memory_required(manifest: dict, context_tokens: int, output_tokens: int) -> int:
    """Runtime estimate only; Go remains the single admission/reservation authority."""
    integer(context_tokens, 1, 4096)
    integer(output_tokens, 1, 512)
    m = manifest["memory"]
    resident = (m["weights_bytes"] + m["kv_bytes_per_token"] * (context_tokens + output_tokens)
                + m["activation_bytes"] + m["buffer_bytes"] + m["safety_bytes"])
    return max(resident, m["load_peak_bytes"] + m["safety_bytes"])


def verify_admission(path: str, required: int, model_digest: str, *, now: float | None = None) -> dict:
    """Consume a local Go-issued snapshot; not a signature, scheduler, or lease service."""
    grant = strict_json(read_regular(absolute_local(path), 64 * 1024, private=True))
    exact_keys(grant, {"schema_version", "attempt_id", "model_manifest_sha256",
                       "reserved_bytes", "available_bytes", "owner_reserve_bytes",
                       "observed_at_unix", "expires_at_unix"})
    if grant["schema_version"] != 1 or grant["model_manifest_sha256"] != model_digest:
        raise Rejected("admission is for a different model")
    if not isinstance(grant["attempt_id"], str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,100}", grant["attempt_id"]):
        raise Rejected("invalid attempt identity")
    for key in ("reserved_bytes", "available_bytes", "owner_reserve_bytes",
                "observed_at_unix", "expires_at_unix"):
        integer(grant[key])
    current = time.time() if now is None else now
    age = current - grant["observed_at_unix"]
    if not 0 <= age <= 15 or not current < grant["expires_at_unix"] <= current + 300:
        raise Rejected("admission/headroom expired or outside the approved window")
    usable = max(0, grant["available_bytes"] - grant["owner_reserve_bytes"])
    if required > min(grant["reserved_bytes"], usable):
        raise Rejected("estimated resident/load-peak memory exceeds admitted headroom")
    return grant


def infer(config_path: str, admission_path: str, prompt_path: str, max_tokens: int = 128) -> dict:
    integer(max_tokens, 1, 512)
    config = config_from_file(config_path)
    # Validation and gating precede any MLX import, including on Linux.
    manifest = verify_model(config["model_directory"], config["model_manifest_sha256"])
    verify_dependencies(config)
    prompt = read_regular(absolute_local(prompt_path), 16 * 1024).decode("utf-8")
    if not prompt.strip():
        raise Rejected("a nonempty UTF-8 prompt is required")
    format_prompt(manifest["model_type"], prompt)  # Reject role injection before loading.
    # Admit worst-case supported context before loading the model. The owner
    # owns this directory; immutable read-only snapshots are a deployment gate.
    required = memory_required(manifest, 4096, max_tokens)
    grant = verify_admission(admission_path, required, config["model_manifest_sha256"])
    os.environ["HF_HUB_OFFLINE"] = "1"
    os.environ["TRANSFORMERS_OFFLINE"] = "1"
    os.environ["HF_DATASETS_OFFLINE"] = "1"
    os.environ["HF_HUB_DISABLE_TELEMETRY"] = "1"
    os.environ.pop("MLXLM_USE_MODELSCOPE", None)
    # No model names, remote URLs, adapters, custom Python, Jinja chat template
    # evaluation, sampler plugins, or generation kwargs are accepted from jobs.
    with redirect_stdout(sys.stderr):
        mx = importlib.import_module("mlx.core")
        if not mx.metal.is_available():
            raise Rejected("Metal is unavailable")
        lm = importlib.import_module("mlx_lm")
        sampler = importlib.import_module("mlx_lm.sample_utils").make_sampler(temp=0.0)
        model, tokenizer = lm.load(
            config["model_directory"],
            tokenizer_config={"trust_remote_code": False, "local_files_only": True, "use_fast": True},
            lazy=False,
        )
        tokens = encode_prompt(manifest["model_type"], tokenizer, prompt)
        if not 1 <= len(tokens) <= 4096:
            raise Rejected("prompt exceeds the approved 4096-token context")
        # Import/load time cannot silently consume the lease. The Go supervisor
        # must additionally terminate the process on owner reclaim or lease loss.
        if time.time() >= grant["expires_at_unix"]:
            raise Rejected("admission expired during model loading")
        text = []
        for response in lm.stream_generate(
            model, tokenizer, prompt=tokens, max_tokens=max_tokens, sampler=sampler
        ):
            if time.time() >= grant["expires_at_unix"]:
                raise Rejected("admission expired during generation")
            text.append(response.text)
    return {
        "schema_version": 1, "template": "mlx-local-text-v1",
        "runtime_version": RUNTIME_VERSION, "attempt_id": grant["attempt_id"],
        "model_manifest_sha256": config["model_manifest_sha256"],
        "text": "".join(text), "estimated_required_bytes": required,
    }
