"""Exercise the archive boundary before any browser or application tool runs."""
import io
import gzip
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('reference-web-archive.py')


class ArchiveTests(unittest.TestCase):
    def archive(self, root, extra=(), omit=None):
        file = root / 'client.tar.gz'
        with tarfile.open(file, 'w:gz') as archive:
            entries = [('index.html', b'<base href="/"><script src="flutter_bootstrap.js"></script>'),
                       ('flutter_bootstrap.js', b'bootstrap'), ('main.dart.js', b'x' * 100001)]
            for name, contents in entries:
                if name == omit:
                    continue
                header = tarfile.TarInfo(name)
                header.size = len(contents)
                archive.addfile(header, io.BytesIO(contents))
            for header, contents in extra:
                archive.addfile(header, io.BytesIO(contents))
        return file

    def run_archive(self, archive, dest, *options):
        prefix = ['rtk', 'proxy'] if shutil.which('rtk') else []
        result = subprocess.run(prefix + ['python3', str(SCRIPT), '--artifact', str(archive), '--destination', str(dest), *options],
                                capture_output=True, text=True)
        if result.returncode:
            self.assertIn('invalid Web archive:', result.stderr)
        return result

    def test_extracts_regular_flutter_shaped_files(self):
        # This fixture proves extraction/structure only, not a compiled application.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            result = self.run_archive(self.archive(root), root / 'site')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / 'site/main.dart.js').stat().st_size, 100001)
            self.assertEqual(json.loads(result.stdout)['files'], 3)

    def test_rejects_unsafe_entries_before_any_extraction(self):
        cases = [('parent', '../outside', tarfile.REGTYPE), ('absolute', '/outside', tarfile.REGTYPE),
                 ('dot', 'a/../outside', tarfile.REGTYPE), ('dotprefix', './asset', tarfile.REGTYPE),
                 ('backslash', 'a\\outside', tarfile.REGTYPE), ('percent', '%2e%2e/outside', tarfile.REGTYPE),
                 ('device', 'CON.txt', tarfile.REGTYPE), ('ads', 'asset:stream', tarfile.REGTYPE),
                 ('empty-part', 'a//outside', tarfile.REGTYPE), ('control', 'asset\n.js', tarfile.REGTYPE),
                 ('trailing-dot', 'asset.', tarfile.REGTYPE), ('case', 'INDEX.HTML', tarfile.REGTYPE),
                 ('duplicate', 'index.html', tarfile.REGTYPE), ('parent-file', 'index.html/child', tarfile.REGTYPE),
                 ('symlink', 'link', tarfile.SYMTYPE), ('hardlink', 'link', tarfile.LNKTYPE),
                 ('fifo', 'pipe', tarfile.FIFOTYPE), ('device-node', 'node', tarfile.CHRTYPE)]
        for label, name, kind in cases:
            with self.subTest(label=label), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                header = tarfile.TarInfo(name)
                header.type = kind
                header.linkname = '/tmp'
                header.size = 1 if kind == tarfile.REGTYPE else 0
                archive = self.archive(root, [(header, b'x')])
                result = self.run_archive(archive, root / 'site')
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertFalse((root / 'site').exists(), 'partial extraction before full validation')
                self.assertFalse((root / 'outside').exists())

    def test_rejects_missing_or_false_entry(self):
        for omit in ['index.html', 'main.dart.js', 'flutter_bootstrap.js']:
            with self.subTest(omit=omit), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                result = self.run_archive(self.archive(root, omit=omit), root / 'site')
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / 'site').exists())
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            header = tarfile.TarInfo('main.dart.js'); header.size = 5
            result = self.run_archive(self.archive(root, [(header, b'small')], omit='main.dart.js'), root / 'site')
            self.assertNotEqual(result.returncode, 0)

    def test_count_and_expansion_limits(self):
        for options in [('--max-files', '2'), ('--max-bytes', '100000')]:
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                result = self.run_archive(self.archive(root), root / 'site', *options)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / 'site').exists())

    def test_rejects_oversized_pax_metadata_before_parsing(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            file = root / 'metadata-bomb.tar.gz'
            header = tarfile.TarInfo('pax'); header.type = tarfile.XHDTYPE; header.size = 2 * 1024 * 1024
            with gzip.open(file, 'wb') as stream:
                stream.write(header.tobuf())
                stream.write(b'0' * header.size)
            result = self.run_archive(file, root / 'site', '--max-files', '3', '--max-bytes', '100000')
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('decompression limit', result.stderr)
            self.assertFalse((root / 'site').exists())

    def test_refuses_existing_or_linked_destination(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve(); archive = self.archive(root)
            (root / 'existing').mkdir(); (root / 'existing/keep').write_text('unchanged')
            (root / 'link').symlink_to(root / 'existing', target_is_directory=True)
            for dest in [root / 'existing', root / 'link', root / 'link/site']:
                result = self.run_archive(archive, dest)
                self.assertNotEqual(result.returncode, 0)
            self.assertEqual((root / 'existing/keep').read_text(), 'unchanged')


if __name__ == '__main__':
    unittest.main()
