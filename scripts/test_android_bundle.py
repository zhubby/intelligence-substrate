"""Bundle archive and manifest boundaries, not a cryptographic signature fixture."""
import importlib.util
from pathlib import Path
import stat
import tempfile
import unittest
import zipfile


class AndroidBundleTests(unittest.TestCase):
    def setUp(self):
        script = Path(__file__).with_name('android_bundle.py')
        self.assertTrue(script.is_file(), 'AAB inspection helper is missing')
        spec = importlib.util.spec_from_file_location('bundle', script)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / 'bundle.aab'
        self.files = {'BundleConfig.pb': b'config', 'base/manifest/AndroidManifest.xml': b'proto',
                      'base/dex/classes.dex': b'dex', 'base/lib/arm64-v8a/libflutter.so': b'flutter',
                      'base/assets/flutter_assets/AssetManifest.bin': b'assets'}
        self.write()

    def write(self, extras=()):
        with zipfile.ZipFile(self.path, 'w') as archive:
            for name, body in [*self.files.items(), *extras]:
                archive.writestr(name, body)

    def test_unsigned_bundle_payload_is_exact_and_requires_bundle_structure(self):
        result = self.module.archive(self.path)
        self.assertEqual(set(result['files']), set(self.files))
        self.assertEqual(len(result['payloadDigest']), 64)
        self.files.pop('BundleConfig.pb')
        self.write()
        with self.assertRaises(ValueError):
            self.module.archive(self.path)

    def test_unsigned_bundle_rejects_signatures_case_collisions_links_and_escape(self):
        for name in ['../escape', '/absolute', 'BASE/dex/classes.dex', 'base/dex/../escape', 'META-INF/UPLOAD.RSA']:
            with self.subTest(name=name):
                self.write([(name, b'bad')])
                with self.assertRaises(ValueError):
                    self.module.archive(self.path)
        linked = zipfile.ZipInfo('linked')
        linked.create_system = 3
        linked.external_attr = (stat.S_IFLNK | 0o777) << 16
        self.write([(linked, b'/outside')])
        with self.assertRaises(ValueError):
            self.module.archive(self.path)

    def test_signed_payload_excludes_only_jar_signature_metadata(self):
        original = self.module.archive(self.path)
        self.write([('META-INF/MANIFEST.MF', b'jar manifest'), ('META-INF/UPLOAD.SF', b'signature'),
                    ('META-INF/UPLOAD.RSA', b'block'), ('BUNDLE-METADATA/custom/source.json', b'protected')])
        result = self.module.archive(self.path, signed=True)
        self.assertEqual(set(result['files']) - set(original['files']), {'BUNDLE-METADATA/custom/source.json'})
        self.assertNotEqual(result['payloadDigest'], original['payloadDigest'])

    def test_other_native_abis_cannot_claim_an_arm64_only_build(self):
        self.write([('base/lib/x86_64/libflutter.so', b'other ABI')])
        with self.assertRaises(ValueError):
            self.module.archive(self.path)

    def test_manifest_requires_exact_identity_version_sdk_and_release_state(self):
        text = '<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="com.example.release" android:versionCode="7" android:versionName="1.2.3"><uses-sdk android:minSdkVersion="24"/><application android:debuggable="false"/></manifest>'
        result = self.module.manifest(text, 'com.example.release')
        self.assertEqual(result, {'packageName': 'com.example.release', 'versionCode': 7, 'versionName': '1.2.3', 'minSDK': 24})
        for bad in [text.replace('com.example.release', 'com.other.release'), text.replace('"7"', '"0"'),
                    text.replace('"24"', '"23"'), text.replace('"false"', '"true"'), text.replace('</manifest>', '<application/></manifest>'), '<manifest/>']:
            with self.subTest(text=bad), self.assertRaises(ValueError):
                self.module.manifest(bad, 'com.example.release')


if __name__ == '__main__':
    unittest.main()
