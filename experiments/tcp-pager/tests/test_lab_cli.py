"""Actual process/transport acceptance of the guided lab, without native HVF.

Build build/nexal-pager-lab first. Tests use explicitly opted-in loopback only.
No Mac, physical LAN, native guest OS or added RAM is simulated as a success.
"""
import json
import os
import selectors
import signal
import socket
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BIN = ROOT / "build/nexal-pager-lab"


class LabCLI(unittest.TestCase):
    def cli(self, *args):
        return subprocess.run([str(BIN), *args], capture_output=True, text=True, timeout=15)

    def start(self, root):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        state = root / "state"
        process = subprocess.Popen(
            [str(BIN), "donor", "--state", str(state),
             "--listen", f"127.0.0.1:{port}", "--loopback-test", "--lifetime", "20s"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        self.addCleanup(self.stop, process)
        # Bounded readiness read; diagnostics contain no private credentials.
        transcript = b""
        with selectors.DefaultSelector() as sel:
            sel.register(process.stderr, selectors.EVENT_READ)
            while b"Keep this terminal open." not in transcript:
                self.assertTrue(sel.select(5), "donor readiness timed out")
                chunk = os.read(process.stderr.fileno(), 4096)
                self.assertTrue(chunk, transcript.decode())
                transcript += chunk
                self.assertLess(len(transcript), 16384)
        m = json.loads((state / "client" / "connection.json").read_text())
        return state, m, process, transcript

    @staticmethod
    def stop(process):
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
        try:
            process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate(timeout=5)

    def receive(self, state, manifest, out, *extra):
        return self.cli("receive", "--bundle", str(state / "client"),
                        "--output", str(out), "--portable-only", "--loopback-test",
                        "--ca-fingerprint", manifest["caSHA256"], *extra)

    def test_resume_existing_state_preserves_credentials(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            state, m, process, _ = self.start(root)
            before = {str(p.relative_to(state)): p.read_bytes()
                      for p in state.rglob("*") if p.is_file()}
            self.stop(process)
            resumed = subprocess.Popen(
                [str(BIN), "serve", "--state", str(state), "--loopback-test",
                 "--lifetime", "20s", "--sessions", "1"],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            self.addCleanup(self.stop, resumed)
            transcript = b""
            with selectors.DefaultSelector() as sel:
                sel.register(resumed.stderr, selectors.EVENT_READ)
                while b"Keep this terminal open." not in transcript:
                    self.assertTrue(sel.select(5), "resume readiness timed out")
                    chunk = os.read(resumed.stderr.fileno(), 4096)
                    self.assertTrue(chunk, transcript.decode())
                    transcript += chunk
            result = self.receive(state, m, root / "resumed-result")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue(json.loads(result.stdout)["pagingSuitePassed"])
            _, tail = resumed.communicate(timeout=5)
            self.assertIn(b'"phase":"tls_handshake"', tail)
            self.assertIn(b'"result":"authenticated"', tail)
            after = {str(p.relative_to(state)): p.read_bytes()
                     for p in state.rglob("*") if p.is_file()}
            self.assertEqual(before, after)

    def test_repeated_process_workloads_and_report(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            state, m, process, diagnostic = self.start(root)
            self.assertNotIn(b"PRIVATE KEY", diagnostic)
            self.assertIn(b"Donor connection: Loopback", diagnostic)
            for n in range(2):
                out = root / f"receiver-{n}"
                result = self.receive(state, m, out)
                self.assertEqual(result.returncode, 0, result.stderr)
                r = json.loads(result.stdout)
                self.assertTrue(r["pagingSuitePassed"])
                self.assertFalse(r["nativeSuiteExecuted"])
                self.assertFalse(r["hostRAMExpansionPassed"])
                self.assertFalse(r["guestOSRAMExpansionPassed"])
                self.assertFalse(r["nonLocalEndpoint"])
                self.assertEqual(json.loads((out / "report.json").read_text()), r)
            process.send_signal(signal.SIGTERM)
            process.communicate(timeout=5)
            self.assertEqual(process.returncode, 0)

    def test_requested_os_ram_is_not_faked(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            state, m, _, _ = self.start(root)
            result = self.receive(state, m, root / "receiver", "--require-os-ram")
            self.assertEqual(result.returncode, 3, result.stderr)
            r = json.loads(result.stdout)
            self.assertTrue(r["pagingSuitePassed"])
            self.assertEqual(r["osMemoryRequirement"], "NOT_IMPLEMENTED")
            self.assertFalse(r["hostRAMExpansionPassed"])
            self.assertIn("NOT IMPLEMENTED", result.stderr)

    def test_wrong_fingerprint_stops_before_connecting(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            state, m, _, _ = self.start(root)
            result = self.receive(state, m, root / "bad", "--ca-fingerprint", "0" * 64)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("fingerprint mismatch", result.stderr)
            self.assertFalse((root / "bad").exists())
            good = self.receive(state, m, root / "good")
            self.assertEqual(good.returncode, 0, good.stderr)

    def test_existing_output_not_overwritten(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            state, m, _, _ = self.start(root)
            out = root / "receiver"
            out.mkdir()
            marker = out / "keep"
            marker.write_text("preserve")
            result = self.receive(state, m, out)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(marker.read_text(), "preserve")

    def test_network_address_details(self):
        human = self.cli("networks")
        self.assertEqual(human.returncode, 0, human.stderr)
        self.assertIn("Connection type | Interface", human.stdout)
        result = self.cli("addresses", "--details")
        self.assertEqual(result.returncode, 0, result.stderr)
        rows = json.loads(result.stdout)
        self.assertIsInstance(rows, list)
        for row in rows:
            self.assertTrue(row["ip"])
            self.assertTrue(row["interface"])
            self.assertTrue(row["connectionType"])
        legacy = self.cli("addresses")
        self.assertEqual(legacy.returncode, 0, legacy.stderr)
        self.assertTrue(all(isinstance(ip, str) for ip in json.loads(legacy.stdout) or []))

    def test_invalid_flags(self):
        for args in [
            ("donor", "--listen", "0.0.0.0:9443"),
            ("donor", "--lifetime", "31m"),
            ("donor", "--sessions", "17"),
            ("receive",), ("serve",), ("unknown",),
        ]:
            with self.subTest(args=args):
                self.assertNotEqual(self.cli(*args).returncode, 0)

    @unittest.skipIf(os.uname().sysname == "Darwin", "Linux native-execution gate")
    def test_local_os_ram_cannot_be_claimed_on_linux(self):
        result = self.cli("local-ram-test", "--native-helper", "/bin/true")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("requires Apple-silicon", result.stderr)
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main(verbosity=2)
