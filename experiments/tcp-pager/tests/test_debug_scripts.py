"""Mocked script orchestration only; no native or LAN acceptance claim."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class DebugScriptsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="pager debug ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        (self.root / "build").mkdir()
        for name in ("debug-lib.sh", "lan-donor-macos.sh",
                     "lan-receiver-macos.sh", "resume-donor-macos.sh"):
            shutil.copyfile(ROOT / "scripts" / name, self.scripts / name)
        (self.scripts / "build-macos.sh").write_text(
            '#!/bin/bash\nprintf "mock build\\n"\nexit "${BUILD_EXIT:-0}"\n')
        binary = self.root / "build/nexal-pager-lab"
        binary.write_text('''#!/bin/bash
if [[ "$1" == networks ]]; then echo "Ethernet | en0"; exit; fi
printf 'mock operation: %s\\n' "$1"
printf 'mock diagnostic\\n' >&2
if [[ "${READ_INPUT:-}" == 1 ]]; then
  IFS= read -r reply
  [[ "$reply" == expected ]] || exit 9
  echo "stdin preserved"
fi
exit "${MOCK_EXIT:-0}"
''')
        binary.chmod(0o700)
        self.state = self.root / "state with spaces"
        for role in ("donor", "client"):
            (self.state / role).mkdir(parents=True)
            (self.state / role / "connection.json").write_text("{}")
        self.env = {**os.environ, "TMPDIR": str(self.root),
                    "SECRET_TEST_TOKEN": "never-dump-this-test-token"}
        for key in ("BASH_ENV", "NEXAL_PAGER_DEBUG_SNAPSHOT"):
            self.env.pop(key, None)

    def run_script(self, name, *args, input=""):
        return subprocess.run(["bash", str(self.scripts / name), *args],
                              env=self.env, input=input, text=True,
                              capture_output=True, timeout=5)

    def test_all_wrappers_accept_debug_and_log_private_live_output(self):
        cases = [
            ("lan-donor-macos.sh", ["--debug"]),
            ("lan-receiver-macos.sh", [str(self.state / "client"), "--debug"]),
            ("resume-donor-macos.sh", ["--debug", str(self.state)]),
        ]
        for script, args in cases:
            result = self.run_script(script, *args)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("mock build", result.stdout)
            self.assertIn("mock diagnostic", result.stdout)
            self.assertIn("Ethernet | en0", result.stdout)
            self.assertIn("[debug] Script exit status: 0", result.stdout)
        logs = list(self.root.glob("nexal-pager-*/debug.log"))
        self.assertEqual(len(logs), 3)
        for log in logs:
            self.assertEqual(log.stat().st_mode & 0o777, 0o600)
            self.assertEqual(log.parent.stat().st_mode & 0o777, 0o700)
            self.assertNotIn(self.env["SECRET_TEST_TOKEN"], log.read_text())

    def test_failure_status_and_prompt_stdin_preserved(self):
        self.env.update(MOCK_EXIT="7", READ_INPUT="1")
        result = self.run_script("lan-receiver-macos.sh", "--debug",
                                 str(self.state / "client"), input="expected\n")
        self.assertEqual(result.returncode, 7)
        self.assertIn("stdin preserved", result.stdout)
        self.assertIn("Script exit status: 7", result.stdout)

    def test_build_failure_stops_before_runtime(self):
        self.env["BUILD_EXIT"] = "6"
        result = self.run_script("lan-donor-macos.sh", "--debug")
        self.assertEqual(result.returncode, 6)
        self.assertNotIn("mock operation", result.stdout)

    def test_no_debug_creates_no_debug_log(self):
        result = self.run_script("lan-receiver-macos.sh", str(self.state / "client"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("[debug]", result.stdout)
        self.assertEqual(list(self.root.glob("nexal-pager-*")), [])

    def test_debug_missing_path_fails_without_runtime(self):
        result = self.run_script("lan-receiver-macos.sh", "--debug")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("mock operation", result.stdout)


if __name__ == "__main__":
    unittest.main()
