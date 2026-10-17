import contextlib
import io
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import l10n  # noqa: E402


class ParseTests(unittest.TestCase):
    def test_roundtrip_with_escapes_and_comments(self):
        text = '/* note */\n"a \\"q\\"" = "b\\nline";\n// c\n"k" = "v";'
        e = l10n.parse_strings(text)
        self.assertEqual(e[0], ('a "q"', "b\nline", "note"))
        self.assertEqual(e[1], ("k", "v", ""))
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "x.lproj", "Localizable.strings")
            l10n.write_strings(p, e)
            self.assertEqual(sorted(l10n.read_strings(p)), sorted(e))

    def test_bad_syntax_is_reported_with_line(self):
        with self.assertRaises(l10n.StringsError) as ctx:
            l10n.parse_strings('"a" = "b"\n"c" = "d";')
        self.assertIn(":2:", str(ctx.exception))
        with self.assertRaises(l10n.StringsError):
            l10n.parse_strings('"a" = "unterminated;')


class ExtractTests(unittest.TestCase):
    def lit(self, swift):
        with tempfile.TemporaryDirectory() as d:
            with open(os.path.join(d, "V.swift"), "w") as f:
                f.write(swift)
            return l10n.extract_swift([d], d)

    def test_finds_ui_literals_and_skips_non_ui(self):
        found = self.lit('''
        Text("Connected")
        Label("Mesh runtime out of date", systemImage: "exclamationmark")
        Button("Check Now") {}
        Text(verbatim: "raw")
        Text(variable)
        Text("•")
        Text("\\(a)")
        let x = String(localized: "Update failed")
        .help("Copy the address")
        view.accessibilityIdentifier("do-not-translate")
        ''')
        self.assertEqual(set(found), {"Connected", "Mesh runtime out of date", "Check Now", "Update failed", "Copy the address"})

    def test_interpolation_becomes_placeholders(self):
        found = self.lit('Text("Version \\(updater.currentVersion) on \\(peers.count) peers")\nText("Hi \\(name(first: "x"))")')
        self.assertIn("Version %@ on %lld peers", found)
        self.assertIn("Hi %@", found)

    def test_escapes_and_multiline_skipped(self):
        found = self.lit('Text("Say \\"hi\\"")\nText("""\nmulti\n""")')
        self.assertEqual(set(found), {'Say "hi"'})

    def test_tests_and_thirdparty_dirs_skipped(self):
        with tempfile.TemporaryDirectory() as d:
            os.makedirs(os.path.join(d, "Tests"))
            with open(os.path.join(d, "Tests", "T.swift"), "w") as f:
                f.write('Text("Only in tests")')
            self.assertEqual(l10n.extract_swift([d], d), {})


class CheckTests(unittest.TestCase):
    def make(self, d, code, entries):
        l10n.write_strings(os.path.join(d, f"{code}.lproj", "Localizable.strings"), entries)

    def run_check(self, en, es):
        with tempfile.TemporaryDirectory() as d:
            self.make(d, "en", en)
            self.make(d, "es", es)
            t = {"strings": d, "plist": None}
            return l10n.check_target("t", t, [{"code": "es"}])

    def test_clean(self):
        e, w = self.run_check([("Hello %@", "Hello %@", "")], [("Hello %@", "Hola %@", "")])
        self.assertEqual((e, w), ([], []))

    def test_placeholder_mismatch_missing_and_empty(self):
        e, _ = self.run_check([("Hello %@", "Hello %@", ""), ("Bye", "Bye", "")], [("Hello %@", "Hola", "")])
        joined = "\n".join(e)
        self.assertIn("placeholders differ", joined)
        self.assertIn("missing key 'Bye'", joined)
        e, _ = self.run_check([("Bye", "Bye", "")], [("Bye", " ", "")])
        self.assertIn("empty translation", "\n".join(e))

    def test_reordered_positional_placeholders_are_allowed(self):
        e, _ = self.run_check([("%@ of %lld", "%@ of %lld", "")], [("%@ of %lld", "%lld de %@", "")])
        self.assertEqual(e, [])
        e, _ = self.run_check([("%@ of %lld", "%@ of %lld", "")], [("%@ of %lld", "%@ de %@", "")])
        self.assertTrue(e)

    def test_todo_is_warning_not_error_and_sync_adds_missing(self):
        with tempfile.TemporaryDirectory() as d:
            self.make(d, "en", [("A", "A", ""), ("B", "B", "")])
            self.make(d, "es", [("A", "Á", "")])
            t = {"strings": d, "plist": None}
            with contextlib.redirect_stdout(io.StringIO()):
                l10n.sync_target("t", t)
            entries = {k: (v, c) for k, v, c in l10n.read_strings(l10n.lproj(t, "es"))}
            self.assertEqual(entries["B"], ("B", l10n.TODO))
            self.assertEqual(entries["A"], ("Á", ""))
            e, w = l10n.check_target("t", t, [{"code": "es"}])
            self.assertEqual(e, [])
            self.assertTrue(any("TODO-TRANSLATE" in x for x in w))


if __name__ == "__main__":
    unittest.main()
