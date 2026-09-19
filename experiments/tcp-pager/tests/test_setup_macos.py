"""Mock-toolchain tests only; not native Apple/Homebrew acceptance."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class SetupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="nexal setup ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        for name in ("setup-lan-macos.sh", "toolchain-lib.sh"):
            shutil.copy2(ROOT / "scripts" / name, self.scripts / name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "actions"
        self.prefix = self.root / "versioned"
        (self.prefix / "bin").mkdir(parents=True)
        self.env = {**os.environ, "PATH": f"{self.bin}:/usr/bin:/bin",
                    "TEST_LOG": str(self.log), "TEST_PREFIX": str(self.prefix)}
        self.env.pop("NEXAL_PAGER_GO", None)
        self.env.pop("BASH_ENV", None)
        self.tool("uname", 'case "$1" in -s) echo Darwin;; -m) echo arm64;; esac')
        self.tool("xcrun", 'echo /mock/apple-sdk')
        self.tool("codesign", 'exit 0')
        self.tool("go", 'echo "go version go1.23.1 darwin/arm64"')
        self.tool("brew", '''
case "$1" in
 install) echo install >> "$TEST_LOG"; exit "${TEST_BREW_FAIL:-0}";;
 --prefix) printf '%s\\n' "$TEST_PREFIX";;
 *) exit 9;;
esac
''')
        self.write(self.prefix / "bin/go", 'echo "go version go1.26.8 darwin/arm64"')
        for name in ("donor", "receiver"):
            self.write(self.scripts / f"lan-{name}-macos.sh",
                       f'printf "{name}:%s\\n" "$*" >> "$TEST_LOG"')

    def write(self, path, body):
        path.write_text("#!/bin/bash\nset -eu\n" + body + "\n")
        path.chmod(0o700)

    def tool(self, name, body):
        self.write(self.bin / name, body)

    def run_setup(self, *args, answer=""):
        return subprocess.run(["/bin/bash", str(self.scripts / "setup-lan-macos.sh"), *args],
                              input=answer, text=True, capture_output=True,
                              env=self.env, timeout=10)

    def actions(self):
        return self.log.read_text() if self.log.exists() else ""

    def test_check_missing_go_no_install(self):
        r = self.run_setup("--donor", "--check")
        self.assertNotEqual(r.returncode, 0)
        self.assertEqual(self.actions(), "")

    def test_decline_and_eof_no_install(self):
        for answer in ("n\n", ""):
            self.assertNotEqual(self.run_setup("--donor", answer=answer).returncode, 0)
        self.assertEqual(self.actions(), "")

    def test_install_then_start(self):
        r = self.run_setup("--donor", answer="y\n")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.actions(), "install\ndonor:\n")
        self.assertIn("No reboot is requested", r.stdout)

    def test_installed_go_skips_install_after_restart(self):
        self.env["NEXAL_PAGER_GO"] = str(self.prefix / "bin/go")
        for _ in range(2):
            r = self.run_setup("--donor", "--no-start")
            self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.actions(), "")

    def test_install_failure_does_not_start(self):
        self.env["TEST_BREW_FAIL"] = "1"
        self.assertNotEqual(self.run_setup("--donor", answer="y\n").returncode, 0)
        self.assertEqual(self.actions(), "install\n")

    def test_bad_installed_version_does_not_start(self):
        self.write(self.prefix / "bin/go", 'echo "go version go1.23.1 darwin/arm64"')
        self.assertNotEqual(self.run_setup("--donor", answer="y\n").returncode, 0)
        self.assertEqual(self.actions(), "install\n")

    def test_bad_apple_tools_no_install(self):
        self.tool("xcrun", "exit 1")
        self.assertNotEqual(self.run_setup("--donor", answer="y\n").returncode, 0)
        self.assertEqual(self.actions(), "")

    def test_explicit_bad_go_no_install(self):
        self.env["NEXAL_PAGER_GO"] = str(self.bin / "go")
        self.assertNotEqual(self.run_setup("--donor", answer="y\n").returncode, 0)
        self.assertEqual(self.actions(), "")

    def test_receiver_path_with_spaces(self):
        bundle = self.root / "client bundle"
        bundle.mkdir()
        self.env["NEXAL_PAGER_GO"] = str(self.prefix / "bin/go")
        r = self.run_setup("--receiver", str(bundle))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.actions(), f"receiver:{bundle}\n")

    def test_network_info_never_starts_donor(self):
        cli = self.root / "mock network cli"
        self.write(cli, 'printf "network-info:%s\\n" "$*" >> "$TEST_LOG"')
        self.env["TEST_NETWORK_CLI"] = str(cli)
        self.write(self.prefix / "bin/go", '''
if [[ "$1" == version ]]; then
  echo "go version go1.26.8 darwin/arm64"
elif [[ "$1" == build && "$3" == -o ]]; then
  cp "$TEST_NETWORK_CLI" "$4"
else
  exit 1
fi
''')
        self.env["NEXAL_PAGER_GO"] = str(self.prefix / "bin/go")
        result = self.run_setup("--network-info")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.actions(), "network-info:networks\n")
        self.assertIn("No listener will start", result.stdout)

    def test_platform_and_argument_guards(self):
        for args in ((), ("--wat",), ("--donor", "--receiver", "/tmp")):
            self.assertNotEqual(self.run_setup(*args).returncode, 0)
        self.tool("uname", "echo Linux")
        self.assertNotEqual(self.run_setup("--donor", answer="y\n").returncode, 0)
        self.assertEqual(self.actions(), "")

    def test_version_parser(self):
        for version, accepted in [
            ("go1.26.0 darwin/arm64", True),
            ("go1.27.1 darwin/arm64", True),
            ("go1.23.1 darwin/arm64", False),
            ("go1.26rc1 darwin/arm64", False),
            ("go1.26.0 darwin/amd64", False),
            ("go1.26.0 linux/arm64", False),
        ]:
            self.write(self.prefix / "bin/go", f'echo "go version {version}"')
            self.env["NEXAL_PAGER_GO"] = str(self.prefix / "bin/go")
            r = self.run_setup("--donor", "--check")
            self.assertEqual(r.returncode == 0, accepted, (version, r.stderr))
        self.assertEqual(self.actions(), "")


if __name__ == "__main__":
    unittest.main()
