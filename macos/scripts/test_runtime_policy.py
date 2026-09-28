import importlib.util
import pathlib
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('policy', pathlib.Path(__file__).with_name('verify-runtime.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class PolicyTests(unittest.TestCase):
    def test_stock_or_modified_source_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'runtime'
            path.write_bytes(b'stock netbird')
            with self.assertRaises(ValueError): m.verify_source(path)
            alias = pathlib.Path(directory) / 'alias'
            alias.symlink_to(path)
            with self.assertRaises(ValueError): m.verify_source(alias)

    def test_invalid_signature_never_executes_runtime(self):
        with patch.object(m, 'run', side_effect=ValueError('bad signature')) as run:
            with self.assertRaises(ValueError): m.verify_app('/candidate.app')
            self.assertEqual(run.call_count, 1)
            self.assertEqual(run.call_args.args[0], '/usr/bin/codesign')

    def test_stock_version_rejected_even_when_signed(self):
        def run(*args):
            if args[0] == '/usr/bin/lipo': return 'arm64 x86_64'
            if args[-1] == 'version': return '0.79.0'
            return ''
        with patch.object(m, 'run', side_effect=run):
            with self.assertRaisesRegex(ValueError, 'Unapproved runtime'): m.verify_app('/candidate.app')

    def test_approved_signed_version(self):
        def run(*args):
            if args[0] == '/usr/bin/lipo': return 'arm64 x86_64'
            if args[-1] == 'version': return m.POLICY['version']
            return ''
        with patch.object(m, 'run', side_effect=run): m.verify_app('/candidate.app')

if __name__ == '__main__': unittest.main()
