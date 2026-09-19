"""Real foreground donor/client subprocess tests with temporary credentials.

Build build/nexal-pager first. No external network and no native HVF execution.
"""
import json
import os
import signal
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BIN = ROOT / "build/nexal-pager"


class CLITests(unittest.TestCase):
    def run_cli(self, *args, ok=True):
        result = subprocess.run([str(BIN), *args], capture_output=True, text=True, timeout=15)
        if ok:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
        return result

    def test_selftest(self):
        result = json.loads(self.run_cli("selftest", "--pages", "16", "--cache", "2").stdout)
        self.assertTrue(result["verified"])
        self.assertFalse(result["nativeHVFExecuted"])
        self.assertEqual(result["cache"]["peakResidentPages"], 2)
        self.assertEqual(result["transport"]["puts"], 16)

    def test_invalid_options(self):
        for args in (("selftest", "--pages", "257"), ("selftest", "--cache", "0"),
                     ("selftest", "--timeout", "3m"), ("donor", "--listen", "0.0.0.0:9999"),
                     ("unknown",), ("client",), ("selftest", "extra")):
            with self.subTest(args=args):
                self.run_cli(*args, ok=False)

    def test_separate_processes_and_teardown(self):
        with tempfile.TemporaryDirectory() as temp:
            keys = Path(temp) / "keys"
            self.run_cli("init", "--dir", str(keys))
            self.run_cli("init", "--dir", str(keys), ok=False)
            donor = subprocess.Popen(
                [str(BIN), "donor", "--pages", "16", "--keys", str(keys / "donor")],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
            )
            try:
                # The test process has a global timeout when invoked in CI.
                ready = json.loads(donor.stdout.readline())
                address = ready["listen"]
                report = json.loads(self.run_cli(
                    "client", "--addr", address, "--keys", str(keys / "client"), "--cache", "2"
                ).stdout)
                self.assertTrue(report["verified"])
                self.assertEqual(report["verifiedBytes"], 16 * 16384)
                # Resuming an already-written donor with fresh version metadata is
                # rejected; the prototype does not silently reconnect.
                self.run_cli("client", "--addr", address, "--keys", str(keys / "client"), ok=False)
                donor.send_signal(signal.SIGTERM)
                stdout, stderr = donor.communicate(timeout=5)
                self.assertEqual(donor.returncode, 0, stderr)
            finally:
                if donor.poll() is None:
                    donor.kill()
                    donor.communicate(timeout=5)

    @unittest.skipIf(os.uname().sysname == "Darwin", "Linux-only fail-closed gate")
    def test_native_rejected_on_linux(self):
        self.run_cli("selftest", "--native-helper", "/bin/true", ok=False)


if __name__ == "__main__":
    unittest.main(verbosity=2)
