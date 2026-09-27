"""Local, pinned input validation. No downloads, remote code, or shell commands."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import stat
from typing import Any

MAX_JSON = 32 * 1024 * 1024
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
WEIGHT = re.compile(r"model(?:-\d{5}-of-\d{5})?\.safetensors\Z")
MODEL_FILES = {
    "config.json", "generation_config.json", "tokenizer.json",
    "tokenizer_config.json", "special_tokens_map.json", "tokenizer.model",
    "model.safetensors.index.json", "added_tokens.json",
}


class Rejected(ValueError):
    """An input failed the approved local workload contract."""


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def absolute_local(path: str) -> Path:
    if not isinstance(path, str) or not path or "\x00" in path:
        raise Rejected("a local absolute path is required")
    p = Path(path)
    if not p.is_absolute() or p != p.resolve(strict=True):
        raise Rejected("absolute paths without symlinks or traversal are required")
    return p


def read_regular(path: Path, limit: int, *, private: bool = False) -> bytes:
    """O_NOFOLLOW and descriptor validation defend the final open, not all TOCTOU."""
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise Rejected("input must be a bounded regular file")
        if info.st_uid not in (os.getuid(), 0) or info.st_mode & 0o022:
            raise Rejected("input must be owner controlled and not writable by other users")
        if private and info.st_mode & 0o077:
            raise Rejected("local configuration and admission files require mode 0600")
        result = stream.read(limit + 1)
        if len(result) > limit:
            raise Rejected("input exceeds size limit")
        return result


def strict_json(data: bytes) -> dict[str, Any]:
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise Rejected("duplicate JSON keys are forbidden")
            result[key] = value
        return result

    def bad_constant(_):
        raise Rejected("non-finite JSON numbers are forbidden")

    try:
        value = json.loads(data, object_pairs_hook=pairs, parse_constant=bad_constant)
    except (ValueError, UnicodeError) as error:
        raise Rejected("invalid JSON") from error
    if not isinstance(value, dict):
        raise Rejected("JSON object required")
    return value


def exact_keys(value: dict, keys: set[str]) -> None:
    if set(value) != keys:
        raise Rejected("missing or unknown contract fields")


def integer(value: Any, minimum: int = 0, maximum: int = 1 << 50) -> int:
    if type(value) is not int or not minimum <= value <= maximum:
        raise Rejected("integer outside approved bounds")
    return value


def check_metadata(value: Any) -> None:
    """Disallow model-supplied Python, external files and custom architectures."""
    if isinstance(value, dict):
        for key, item in value.items():
            if key in {"auto_map", "model_file", "trust_remote_code", "_name_or_path"}:
                raise Rejected("custom model code and remote metadata are not approved")
            if key.endswith(("_file", "_path")):
                raise Rejected("model metadata cannot redirect local file loading")
            check_metadata(item)
    elif isinstance(value, list):
        for item in value:
            check_metadata(item)


def verify_model(directory: str, expected_manifest_sha256: str) -> dict:
    from .profiles import TOKENIZERS, validate_config

    root = absolute_local(directory)
    if not root.is_dir() or root.stat().st_mode & 0o022:
        raise Rejected("model directory must be owner-controlled")
    if not SHA256.fullmatch(expected_manifest_sha256):
        raise Rejected("a pinned model manifest SHA-256 is required")
    manifest_data = read_regular(root / "nexal-model-manifest.json", MAX_JSON)
    if digest(manifest_data) != expected_manifest_sha256:
        raise Rejected("model manifest digest mismatch")
    manifest = strict_json(manifest_data)
    exact_keys(manifest, {"schema_version", "model_id", "revision", "license",
                          "model_type", "files", "memory"})
    if manifest["schema_version"] != 1 or manifest["model_type"] not in TOKENIZERS:
        raise Rejected("only schema 1 and reviewed built-in architectures are approved")
    if not isinstance(manifest["revision"], str) or not re.fullmatch(
        r"[0-9a-f]{40,64}", manifest["revision"]
    ):
        raise Rejected("model revision must be an immutable content revision")
    for key in ("model_id", "license"):
        if not isinstance(manifest[key], str) or not 1 <= len(manifest[key]) <= 200:
            raise Rejected("model identity and license are required")
    files = manifest["files"]
    if not isinstance(files, dict) or not 3 <= len(files) <= 2048:
        raise Rejected("invalid model file manifest")
    actual = {p.name for p in root.iterdir()}
    if actual != set(files) | {"nexal-model-manifest.json"}:
        raise Rejected("model directory contains unpinned or missing files")
    if not {"config.json", "tokenizer_config.json"} <= set(files):
        raise Rejected("model and tokenizer configuration required")
    if not any(WEIGHT.fullmatch(name) for name in files):
        raise Rejected("approved safetensors weights required")
    if not ({"tokenizer.json", "tokenizer.model"} & set(files)):
        raise Rejected("local tokenizer required")
    if manifest["model_type"] != "llama" and "tokenizer.json" not in files:
        raise Rejected("Qwen/Phi require a pinned local fast tokenizer")
    for name, expected in files.items():
        if name not in MODEL_FILES and not WEIGHT.fullmatch(name):
            raise Rejected("model file type is not allowlisted")
        if not isinstance(expected, str) or not SHA256.fullmatch(expected):
            raise Rejected("each model file requires a SHA-256 digest")
        path = root / name
        if path.is_symlink() or not path.is_file():
            raise Rejected("model files must be regular files, not links")
        # Stream weights instead of loading a multi-GB weight file into Python.
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        with os.fdopen(fd, "rb") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o022:
                raise Rejected("unsafe model file permissions")
            hasher = hashlib.sha256()
            while chunk := stream.read(1024 * 1024):
                hasher.update(chunk)
            if hasher.hexdigest() != expected:
                raise Rejected("model file integrity check failed")
        if name.endswith(".json") and name != "tokenizer.json":
            metadata = strict_json(read_regular(path, MAX_JSON))
            check_metadata(metadata)
            if name == "config.json":
                validate_config(manifest["model_type"], metadata)
            if name == "tokenizer_config.json" and metadata.get("tokenizer_class") not in TOKENIZERS[manifest["model_type"]]:
                raise Rejected("tokenizer implementation is not approved")
            if name == "model.safetensors.index.json":
                weight_map = metadata.get("weight_map", {})
                if not isinstance(weight_map, dict) or not weight_map or any(
                    target not in files or not WEIGHT.fullmatch(target)
                    for target in weight_map.values()
                ):
                    raise Rejected("weight index references an unpinned file")
    memory = manifest["memory"]
    if not isinstance(memory, dict):
        raise Rejected("memory estimates required")
    exact_keys(memory, {"weights_bytes", "kv_bytes_per_token", "activation_bytes",
                        "buffer_bytes", "load_peak_bytes", "safety_bytes"})
    for value in memory.values():
        integer(value, minimum=1)
    weights_size = sum((root / name).stat().st_size for name in files if WEIGHT.fullmatch(name))
    if memory["weights_bytes"] < weights_size or memory["load_peak_bytes"] < memory["weights_bytes"]:
        raise Rejected("memory estimates cannot understate stored weights or load peak")
    return manifest
