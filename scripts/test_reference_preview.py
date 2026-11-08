import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest


class ReferencePreviewTest(unittest.TestCase):
    def load(self):
        spec = importlib.util.spec_from_file_location('reference_preview', Path(__file__).with_name('reference_preview.py'))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_preview_package_removes_build_metadata_keeps_exact_executable_and_relative_entry(self):
        module = self.load()
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); built = root / 'web'; built.mkdir()
            (built / 'index.html').write_bytes(b'<html><base href="/"><script src="flutter_bootstrap.js"></script></html>')
            (built / 'main.dart.js').write_bytes(b'compiled-client')
            (built / '.last_build_id').write_text('not a deployable asset')
            backend = root / 'backend'; backend.write_bytes(b'backend-executable')
            entries = dict(module.entries(built, backend, 'booking', 'arm64'))
            self.assertNotIn('.last_build_id', entries)
            self.assertNotIn(b'<base', entries['index.html'])
            self.assertEqual(entries['substrate-preview/backend'], b'backend-executable')
            manifest = json.loads(entries['substrate-preview/manifest.json'])
            self.assertEqual(manifest['backendSHA256'], hashlib.sha256(b'backend-executable').hexdigest())
            self.assertEqual(manifest['backendSize'], 18)
            self.assertEqual(manifest['profile'], 'flutter-go-v1')
            self.assertEqual(manifest['mode'], 'booking')

    def test_preview_packaging_rejects_links_or_unexpected_hidden_files(self):
        module = self.load()
        for kind in ('link', 'hidden'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as d:
                root=Path(d); web=root/'web'; web.mkdir()
                (web/'index.html').write_text('<base href="/">')
                backend=root/'backend'; backend.write_bytes(b'bytes')
                if kind=='link': (web/'escape').symlink_to(backend)
                else: (web/'.env').write_text('secret')
                with self.assertRaises(ValueError): list(module.entries(web, backend, 'feedback', 'arm64'))


if __name__ == '__main__': unittest.main()
