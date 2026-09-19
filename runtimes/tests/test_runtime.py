"""All tests use stdlib only; fixture weights are not an actual MLX model."""

import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch
from types import SimpleNamespace

from nexal_mlx import cli, runtime
from nexal_mlx.ring import smoke, validate_hostfile
from nexal_mlx.security import Rejected, absolute_local, digest, strict_json, verify_model


class LocalFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.model = self.root / "model"
        self.model.mkdir(mode=0o700)
        self.files = {
            "config.json": b'{"model_type":"llama"}',
            "tokenizer_config.json": b'{"tokenizer_class":"PreTrainedTokenizerFast"}',
            "tokenizer.json": b'{"version":"1.0"}',
            "model.safetensors": b"FIXTURE ONLY NOT REAL WEIGHTS",
        }
        self.manifest = {
            "schema_version": 1, "model_id": "unit-test-only", "revision": "a" * 40,
            "license": "unit-test-fixture", "model_type": "llama", "files": {},
            "memory": {"weights_bytes": 1024, "kv_bytes_per_token": 2,
                       "activation_bytes": 100, "buffer_bytes": 100,
                       "load_peak_bytes": 2000, "safety_bytes": 256},
        }
        self.pin_model()

    def write(self, path, data):
        path.write_bytes(data)
        path.chmod(0o600)
        return str(path)

    def jsonfile(self, name, value):
        return self.write(self.root / name, json.dumps(value, sort_keys=True).encode())

    def pin_model(self):
        for name, value in self.files.items():
            self.write(self.model / name, value)
        self.manifest["files"] = {name: digest(value) for name, value in self.files.items()}
        data = json.dumps(self.manifest, sort_keys=True).encode()
        self.write(self.model / "nexal-model-manifest.json", data)
        self.model_hash = digest(data)

    def verify(self):
        return verify_model(str(self.model), self.model_hash)

    def admission(self, **overrides):
        grant = {
            "schema_version": 1, "attempt_id": "test-attempt",
            "model_manifest_sha256": self.model_hash,
            "reserved_bytes": 100_000, "available_bytes": 120_000,
            "owner_reserve_bytes": 10_000,
            "observed_at_unix": 1000, "expires_at_unix": 1100,
        }
        grant.update(overrides)
        return self.jsonfile("admission.json", grant)


class SecurityTests(LocalFixture):
    def test_valid_pinned_fixture(self):
        self.assertEqual(self.verify()["model_id"], "unit-test-only")

    def test_digest_corruption(self):
        self.write(self.model / "model.safetensors", b"corrupted")
        with self.assertRaises(Rejected):
            self.verify()

    def test_unpinned_file(self):
        self.write(self.model / "extra.json", b"{}")
        with self.assertRaises(Rejected):
            self.verify()

    def test_arbitrary_python_not_allowed_even_if_pinned(self):
        self.files["model.py"] = b"raise SystemExit('not executed')"
        self.pin_model()
        with self.assertRaises(Rejected):
            self.verify()

    def test_symlink_refused(self):
        target = self.model / "model.safetensors"
        target.unlink()
        target.symlink_to(self.root / "outside")
        self.write(self.root / "outside", self.files["model.safetensors"])
        with self.assertRaises(Rejected):
            self.verify()

    def test_manifest_digest_mismatch(self):
        with self.assertRaises(Rejected):
            verify_model(str(self.model), "0" * 64)

    def test_remote_and_relative_paths_refused(self):
        for path in ("https://example.test/model", "organization/model", "../model"):
            with self.subTest(path=path), self.assertRaises(Rejected):
                absolute_local(path)

    def test_remote_code_metadata_refused(self):
        for extra in ({"auto_map": {"AutoModel": "custom.Code"}},
                      {"nested": {"trust_remote_code": True}},
                      {"model_file": "model.py"}, {"vocab_file": "/etc/passwd"},
                      {"_name_or_path": "remote/model"}):
            with self.subTest(extra=extra):
                self.files["config.json"] = json.dumps({"model_type": "llama", **extra}).encode()
                self.pin_model()
                with self.assertRaises(Rejected):
                    self.verify()

    def test_wrong_architecture_or_tokenizer_refused(self):
        self.files["config.json"] = b'{"model_type":"custom"}'
        self.pin_model()
        with self.assertRaises(Rejected):
            self.verify()
        self.files["config.json"] = b'{"model_type":"llama"}'
        self.files["tokenizer_config.json"] = b'{"tokenizer_class":"CustomTokenizer"}'
        self.pin_model()
        with self.assertRaises(Rejected):
            self.verify()

    def test_weight_index_external_reference_refused(self):
        self.files["model.safetensors.index.json"] = b'{"weight_map":{"x":"../../evil.safetensors"}}'
        self.pin_model()
        with self.assertRaises(Rejected):
            self.verify()

    def test_duplicate_json_and_non_finite_numbers(self):
        for value in (b'{"a":1,"a":2}', b'{"a":NaN}', b'{"a":Infinity}', b"[]"):
            with self.subTest(value=value), self.assertRaises(Rejected):
                strict_json(value)

    def test_manifest_revision_is_immutable(self):
        self.manifest["revision"] = "main"
        self.pin_model()
        with self.assertRaises(Rejected):
            self.verify()

    def test_low_memory_estimate_refused(self):
        self.manifest["memory"]["weights_bytes"] = 1
        self.pin_model()
        with self.assertRaises(Rejected):
            self.verify()

    def test_world_writable_model_refused(self):
        (self.model / "config.json").chmod(0o666)
        with self.assertRaises(Rejected):
            self.verify()


class AdmissionTests(LocalFixture):
    def test_memory_includes_all_components_and_load_peak(self):
        self.assertEqual(runtime.memory_required(self.manifest, 100, 10), 2256)
        self.assertEqual(runtime.memory_required(self.manifest, 4096, 128), 9928)

    def test_grant_accepts_fresh_headroom(self):
        grant = runtime.verify_admission(self.admission(), 1000, self.model_hash, now=1001)
        self.assertEqual(grant["attempt_id"], "test-attempt")

    def test_grant_rejects_expired_future_or_stale(self):
        for override in ({"expires_at_unix": 1000}, {"observed_at_unix": 999},
                         {"observed_at_unix": 1016}, {"expires_at_unix": 99999}):
            with self.subTest(override=override), self.assertRaises(Rejected):
                runtime.verify_admission(self.admission(**override), 1000, self.model_hash, now=1015)

    def test_grant_bound_to_model_and_owner_memory(self):
        for override in ({"model_manifest_sha256": "0" * 64}, {"reserved_bytes": 999},
                         {"available_bytes": 10999}, {"owner_reserve_bytes": 120001},
                         {"reserved_bytes": True}, {"available_bytes": -1}):
            with self.subTest(override=override), self.assertRaises(Rejected):
                runtime.verify_admission(self.admission(**override), 1000, self.model_hash, now=1001)

    def test_grant_must_be_private(self):
        path = self.admission()
        Path(path).chmod(0o644)
        with self.assertRaises(Rejected):
            runtime.verify_admission(path, 1000, self.model_hash, now=1001)


class CLITests(LocalFixture):
    def test_probe_does_not_import_mlx(self):
        with patch.object(runtime.importlib, "import_module") as importer:
            result = runtime.probe()
            importer.assert_not_called()
        self.assertFalse(result["backend_imported"])
        self.assertEqual(result["distributed_execution"], "disabled")

    def test_cli_probe_clean_json(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(cli.main(["probe"]), 0)
        self.assertEqual(json.loads(output.getvalue())["schema_version"], 1)

    def test_rejects_unallowlisted_flags_and_abbreviations(self):
        for extra in ("--model", "--trust-remote-code", "--adapter-path", "--max-t"):
            with self.subTest(extra=extra), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    cli.parser().parse_args(["infer", "--config", "/tmp/a", "--admission",
                                             "/tmp/b", "--prompt-file", "/tmp/c", extra, "x"])

    def test_isolated_entrypoint_runs_without_pythonpath(self):
        entry = Path(__file__).resolve().parents[1] / "nexal_mlx_entry.py"
        result = subprocess.run([sys.executable, "-I", str(entry), "probe"],
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(json.loads(result.stdout)["backend_imported"])

    def test_dependency_gate_rejects_linux_without_importing_mlx(self):
        with patch.object(runtime.platform, "system", return_value="Linux"):
            with self.assertRaises(Rejected):
                runtime.verify_dependencies({})

    def test_infer_rejects_missing_model_without_importing_mlx(self):
        config = self.jsonfile("config.json", {
            "schema_version": 1, "model_directory": str(self.model),
            "model_manifest_sha256": "0" * 64, "dependency_receipt": "/invalid",
            "dependency_receipt_sha256": "0" * 64,
        })
        with patch.object(runtime.importlib, "import_module") as importer:
            with self.assertRaises(Rejected):
                runtime.infer(config, "/invalid", "/invalid")
            importer.assert_not_called()


class RingTests(LocalFixture):
    def hosts(self, peers=None):
        data = json.dumps(peers or [["192.168.1.10:5100"], ["192.168.1.11:5100"]]).encode()
        path = self.write(self.root / "hosts.json", data)
        return path, digest(data)

    def test_hostfile_valid(self):
        path, sha = self.hosts()
        self.assertEqual(len(validate_hostfile(path, sha, 1)), 2)

    def test_public_wildcard_loopback_and_hostname_rejected(self):
        for host in ("0.0.0.0", "8.8.8.8", "127.0.0.1", "169.254.1.1", "host.local", "::1"):
            path, sha = self.hosts([[host + ":5100"], ["192.168.1.11:5100"]])
            with self.subTest(host=host), self.assertRaises(Rejected):
                validate_hostfile(path, sha, 0)

    def test_hostfile_rank_integrity_duplicates_and_port(self):
        path, sha = self.hosts()
        with self.assertRaises(Rejected):
            validate_hostfile(path, "0" * 64, 0)
        with self.assertRaises(Rejected):
            validate_hostfile(path, sha, 2)
        for peers in ([["192.168.1.10:22"], ["192.168.1.11:5100"]],
                      [["192.168.1.10:5100"], ["192.168.1.10:5100"]]):
            path, sha = self.hosts(peers)
            with self.assertRaises(Rejected):
                validate_hostfile(path, sha, 0)

    def test_smoke_requires_opt_in_before_loading_anything(self):
        with self.assertRaises(Rejected):
            smoke("/none", "/none", "0" * 64, 0)


class DependencyGateTests(LocalFixture):
    def receipt(self, **overrides):
        value = {
            "schema_version": 1, "macos_hardware_smoke_passed": True,
            "python_version": "3.11.9",
            "packages": {"mlx": "0.29.3", "mlx-lm": "0.28.4", "example-dependency": "1.2.3"},
            "requirements_lock_sha256": "a" * 64,
            "wheel_sha256": {"mlx": "b" * 64, "mlx-lm": "c" * 64, "example-dependency": "d" * 64},
        }
        value.update(overrides)
        path = self.jsonfile("receipt.json", value)
        return {"dependency_receipt": path, "dependency_receipt_sha256": digest(Path(path).read_bytes())}

    def verify_receipt(self, config, extra=()):
        installed = [
            SimpleNamespace(metadata={"Name": name}, version=version)
            for name, version in (("mlx", "0.29.3"), ("mlx-lm", "0.28.4"),
                                  ("example-dependency", "1.2.3"), *extra)
        ]
        with patch.object(runtime.platform, "system", return_value="Darwin"), \
             patch.object(runtime.platform, "machine", return_value="arm64"), \
             patch.object(runtime.platform, "python_version", return_value="3.11.9"), \
             patch.object(runtime.metadata, "distributions", return_value=installed):
            runtime.verify_dependencies(config)

    def test_exact_complete_inventory_accepted(self):
        self.verify_receipt(self.receipt())

    def test_extra_or_duplicate_installed_distribution_refused(self):
        for extra in ((("unexpected", "1.0"),), (("mlx", "0.29.3"),)):
            with self.subTest(extra=extra), self.assertRaises(Rejected):
                self.verify_receipt(self.receipt(), extra)

    def test_unapproved_receipt_wrong_python_and_missing_hash_refused(self):
        for override in ({"macos_hardware_smoke_passed": False},
                         {"python_version": "3.11.8"},
                         {"requirements_lock_sha256": "missing"},
                         {"wheel_sha256": {"mlx": "a" * 64}}):
            with self.subTest(override=override), self.assertRaises(Rejected):
                self.verify_receipt(self.receipt(**override))

    def test_receipt_digest_mismatch_refused(self):
        config = self.receipt()
        config["dependency_receipt_sha256"] = "0" * 64
        with self.assertRaises(Rejected):
            self.verify_receipt(config)


class WorkloadIntegrityTests(unittest.TestCase):
    def test_workload_manifest_pins_all_runtime_sources(self):
        root = Path(__file__).resolve().parents[1]
        manifest = json.loads((root / "manifests" / "mlx-local-text-v1.json").read_text())
        self.assertEqual(manifest["schema_version"], 1)
        self.assertFalse(manifest["coordinator_dispatch_enabled"])
        actual = {str(path.relative_to(root)) for path in (root / "nexal_mlx").glob("*.py")}
        actual.add("nexal_mlx_entry.py")
        self.assertEqual(actual, set(manifest["source_sha256"]))
        for path, expected in manifest["source_sha256"].items():
            self.assertEqual(hashlib.sha256((root / path).read_bytes()).hexdigest(), expected, path)
        self.assertEqual(hashlib.sha256((root / "requirements.in").read_bytes()).hexdigest(),
                         manifest["requirements_input_sha256"])
        ring = json.loads((root / "manifests" / "mlx-ring-smoke-v1.json").read_text())
        for path_key, hash_key in (("entrypoint", "entrypoint_sha256"),
                                   ("implementation", "implementation_sha256")):
            self.assertEqual(hashlib.sha256((root / ring[path_key]).read_bytes()).hexdigest(), ring[hash_key])


if __name__ == "__main__":
    unittest.main()
