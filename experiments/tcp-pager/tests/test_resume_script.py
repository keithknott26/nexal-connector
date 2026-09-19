"""Portable shell-wrapper tests; no real donor, Mac or TLS success simulated."""
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ResumeScriptTests(unittest.TestCase):
    def fixture(self, root, exit_code=0):
        scripts = root / "scripts"
        scripts.mkdir()
        build = root / "build"
        build.mkdir()
        shutil.copyfile(ROOT / "scripts/resume-donor-macos.sh",
                        scripts / "resume-donor-macos.sh")
        (scripts / "build-macos.sh").write_text("#!/bin/bash\nexit 0\n")
        binary = build / "nexal-pager-lab"
        binary.write_text(
            "#!/bin/bash\n"
            'printf \'{"event":"donor_connection","phase":"tcp_accept",'
            '"result":"accepted"}\\n\' >&2\n'
            f"exit {exit_code}\n"
        )
        binary.chmod(0o700)
        state = root / "state with spaces"
        for role in ("donor", "client"):
            (state / role).mkdir(parents=True)
            (state / role / "connection.json").write_text("{}")
        return scripts / "resume-donor-macos.sh", state

    def test_private_log_captures_stderr_and_preserves_state(self):
        with tempfile.TemporaryDirectory() as temp:
            script, state = self.fixture(Path(temp))
            before = {p: p.read_bytes() for p in state.rglob("connection.json")}
            outputs = ""
            for _ in range(2):
                result = subprocess.run(["bash", str(script), str(state)],
                                        capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('"phase":"tcp_accept"', result.stdout)
                outputs += result.stdout
            logs = list(state.glob("donor-diagnostics.*"))
            self.assertEqual(len(logs), 2)
            for log in logs:
                self.assertEqual(log.stat().st_mode & 0o777, 0o600)
                self.assertIn('"result":"accepted"', log.read_text())
                self.assertIn(str(log), outputs)
            self.assertEqual(before, {p: p.read_bytes() for p in before})

    def test_donor_failure_is_not_hidden_by_tee(self):
        with tempfile.TemporaryDirectory() as temp:
            script, state = self.fixture(Path(temp), exit_code=7)
            result = subprocess.run(["bash", str(script), str(state)],
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 7)
            self.assertEqual(len(list(state.glob("donor-diagnostics.*"))), 1)

    def test_missing_state_creates_no_log(self):
        with tempfile.TemporaryDirectory() as temp:
            script, state = self.fixture(Path(temp))
            (state / "donor/connection.json").unlink()
            result = subprocess.run(["bash", str(script), str(state)],
                                    capture_output=True, text=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(list(state.glob("donor-diagnostics.*")), [])


if __name__ == "__main__":
    unittest.main()
