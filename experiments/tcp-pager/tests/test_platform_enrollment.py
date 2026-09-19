"""Linux mocks exercise shell orchestration, not native Keychain or enrollment."""
import json
import os
from pathlib import Path
import pty
import select
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
import unittest

SOURCE = Path(__file__).resolve().parents[1] / "scripts"
ORIGIN = "https://nexal-coordinator-dev.nexal.systems"


class EnrollmentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="nexal enrollment ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.lab = self.root / "experiments/tcp-pager"
        self.scripts = self.lab / "scripts"
        self.scripts.mkdir(parents=True)
        (self.root / "connector").mkdir()
        self.bin = self.root / "tools"
        self.bin.mkdir()
        self.home = self.root / "home"
        self.home.mkdir()
        self.config = self.home / "Library/Application Support/Nexal/config.json"
        self.log = self.root / "actions"
        self.env = {**os.environ, "HOME": str(self.home),
                    "PATH": f"{self.bin}:/usr/bin:/bin", "TEST_LOG": str(self.log)}
        for key in ("BASH_ENV", "ENV", "NEXAL_PAGER_GO"):
            self.env.pop(key, None)
        for name in ("setup-lan-macos.sh", "toolchain-lib.sh", "enroll-platform-macos.sh"):
            shutil.copy2(SOURCE / name, self.scripts / name)
        # The release uses Apple's absolute plutil. Replace only in this isolated
        # fixture, so Linux can exercise its return values without native claims.
        helper = self.scripts / "enroll-platform-macos.sh"
        helper.write_text(helper.read_text().replace("/usr/bin/plutil", shlex.quote(str(self.bin / "plutil"))))
        self.script("uname", 'case "$1" in -s) echo Darwin;; -m) echo arm64;; esac')
        self.script("xcrun", "echo /mock/sdk")
        self.script("codesign", "exit 0")
        self.python("plutil", """
import json, sys
try:
    value = json.load(open(sys.argv[-1]))[sys.argv[2]]
    if not isinstance(value, str): sys.exit(1)
    print(value)
except (KeyError, ValueError, OSError): sys.exit(1)
""")
        self.python("mock-nexal", """
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
with open(os.environ["TEST_LOG"], "a") as log: log.write(json.dumps(args) + "\\n")
path = Path(args[args.index("--config") + 1])
if args[0] == "init":
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({"coordinator": args[args.index("--coordinator")+1], "name": args[args.index("--name")+1]}))
elif args[0] == "enroll":
    code = sys.stdin.read().strip()
    if code != "test-secret-invitation": sys.exit(1)
    if os.environ.get("TEST_ENROLL_FAIL"): sys.exit(1)
    data = json.loads(path.read_text())
    data["hostId"] = "host_test"
    path.write_text(json.dumps(data))
    print('{"enrolled":true}')
""")
        self.python("go", """
import os, shutil, sys
from pathlib import Path
if sys.argv[1] == "version": print("go version go1.26.8 darwin/arm64")
elif sys.argv[1] == "build":
    if os.environ.get("TEST_BUILD_FAIL"): sys.exit(1)
    shutil.copy2(Path(__file__).with_name("mock-nexal"), sys.argv[sys.argv.index("-o")+1])
else: sys.exit(1)
""")
        self.env["NEXAL_PAGER_GO"] = str(self.bin / "go")

    def script(self, name, body):
        path = self.bin / name
        path.write_text("#!/bin/bash\nset -eu\n" + body + "\n")
        path.chmod(0o700)

    def python(self, name, body):
        path = self.bin / name
        path.write_text(f"#!{sys.executable}\n" + body)
        path.chmod(0o700)

    def command(self, *args):
        return ["/bin/bash", str(self.scripts / "setup-lan-macos.sh"),
                "--enroll-platform", *args]

    def run_script(self, *args):
        return subprocess.run(self.command(*args), input="", text=True,
                              capture_output=True, env=self.env, timeout=10)

    def existing(self, **fields):
        self.config.parent.mkdir(parents=True, exist_ok=True)
        self.config.write_text(json.dumps({"coordinator": ORIGIN, **fields}))

    def test_prepare_rerun_preserves_configuration(self):
        result = self.run_script("--name", "M4 mini", "--prepare-only")
        self.assertEqual(result.returncode, 0, result.stderr)
        before = self.config.read_bytes()
        result = self.run_script("--name", "M4 mini", "--prepare-only")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(before, self.config.read_bytes())
        actions = self.log.read_text()
        self.assertEqual(len(actions.splitlines()), 1)
        self.assertIn("M4 mini", actions)
        self.assertEqual([json.loads(line)[0] for line in actions.splitlines()], ["init"])

    def test_recorded_enrollment_does_not_consume_code(self):
        self.existing(hostId="host_existing", paused=False)
        before = self.config.read_bytes()
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("already recorded", result.stdout)
        self.assertFalse(self.log.exists())
        self.assertEqual(before, self.config.read_bytes())

    def test_wrong_coordinator_stops_before_build(self):
        self.existing(coordinator="http://127.0.0.1:8787")
        result = self.run_script("--prepare-only")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("differs", result.stderr)
        self.assertFalse((self.lab / "build").exists())

    def test_no_terminal_does_not_enroll(self):
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("terminal", result.stderr)
        self.assertEqual(len(self.log.read_text().splitlines()), 1)

    def test_build_failure_preserves_config(self):
        self.existing()
        before = self.config.read_bytes()
        self.env["TEST_BUILD_FAIL"] = "1"
        self.assertNotEqual(self.run_script().returncode, 0)
        self.assertEqual(before, self.config.read_bytes())
        self.assertFalse(self.log.exists())
        self.assertEqual(list((self.lab / "build").glob(".enroll-build.*")), [])

    def test_symlink_config_rejected(self):
        self.existing()
        target = self.root / "target"
        self.config.rename(target)
        self.config.symlink_to(target)
        self.assertNotEqual(self.run_script().returncode, 0)
        self.assertFalse(self.log.exists())

    def test_argument_guards(self):
        for args in (("--config", "relative"), ("--coordinator", "http://host"),
                     ("--name",), ("--unknown",), ("--profile", "../escape"),
                     ("--profile", ".hidden"), ("--profile", "a" * 49),
                     ("--profile", "bad name"), ("--profile", "lan", "--config", "/tmp/config")):
            self.assertNotEqual(self.run_script(*args).returncode, 0)
        self.assertFalse(self.log.exists())

    def test_named_profile_preserves_default_identity(self):
        self.existing(hostId="host_original", name="Original Mac")
        before = self.config.read_bytes()
        result = self.run_script("--profile", "private-lan", "--name", "M4 mini", "--prepare-only")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(before, self.config.read_bytes())
        profile = self.home / "Library/Application Support/Nexal-Profiles/private-lan/config.json"
        self.assertEqual(json.loads(profile.read_text())["name"], "M4 mini")
        self.assertNotIn("hostId", json.loads(profile.read_text()))
        saved = profile.read_bytes()
        result = self.run_script("--profile", "private-lan", "--name", "M4 mini", "--prepare-only")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(saved, profile.read_bytes())
        self.assertEqual(len(self.log.read_text().splitlines()), 1)

    def test_recorded_name_mismatch_is_not_success(self):
        self.existing(hostId="host_existing", name="M4 mini")
        before = self.config.read_bytes()
        result = self.run_script("--name", "M2 mini")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Saved host ID: host_existing", result.stdout)
        self.assertIn("Saved name: M4 mini", result.stdout)
        self.assertIn("differs", result.stderr)
        self.assertEqual(before, self.config.read_bytes())
        self.assertFalse(self.log.exists())

    def test_symlink_profile_parent_rejected(self):
        base = self.home / "Library/Application Support/Nexal-Profiles"
        base.mkdir(parents=True)
        target = self.root / "elsewhere"
        target.mkdir()
        (base / "private-lan").symlink_to(target, target_is_directory=True)
        result = self.run_script("--profile", "private-lan", "--prepare-only")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("symlink", result.stderr)
        self.assertFalse((target / "config.json").exists())
        self.assertFalse(self.log.exists())

    def test_hidden_terminal_enrollment_and_failure_retry(self):
        for fail in (True, False):
            if fail:
                self.env["TEST_ENROLL_FAIL"] = "1"
            else:
                self.env.pop("TEST_ENROLL_FAIL", None)
            master, slave = pty.openpty()
            process = subprocess.Popen(self.command(), stdin=slave, stdout=slave,
                                       stderr=slave, env=self.env)
            os.close(slave)
            output = b""
            sent = False
            deadline = time.monotonic() + 10
            try:
                while time.monotonic() < deadline:
                    if select.select([master], [], [], 0.1)[0]:
                        try:
                            chunk = os.read(master, 65536)
                        except OSError:
                            break
                        if not chunk:
                            break
                        output += chunk
                    if b"input hidden): " in output and not sent:
                        # Wait for bash read -s to change terminal echo.
                        import termios
                        if not termios.tcgetattr(master)[3] & termios.ECHO:
                            os.write(master, b"test-secret-invitation\n")
                            sent = True
                    if process.poll() is not None:
                        break
                self.assertEqual(process.wait(timeout=2) == 0, not fail, output)
                self.assertTrue(sent)
                self.assertNotIn(b"test-secret-invitation", output)
                self.assertNotIn("test-secret-invitation", self.log.read_text())
                data = json.loads(self.config.read_text())
                self.assertEqual("hostId" in data, not fail)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()
                os.close(master)


if __name__ == "__main__":
    unittest.main()
