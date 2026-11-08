import importlib.util
import io
import json
from pathlib import Path
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import unittest
import warnings
import zipfile


SCRIPT = Path(__file__).with_name('verify-reference-android-quality.py')


class AndroidQualitySafetyTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SCRIPT.is_file(), 'single-application artifact-bound Android quality adapter is missing')
        spec = importlib.util.spec_from_file_location('android_quality', SCRIPT)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def apk(self, extras=()):
        apk = self.root / 'package.apk'
        with zipfile.ZipFile(apk, 'w') as archive:
            for name in ['AndroidManifest.xml', 'classes.dex', 'lib/arm64-v8a/libflutter.so', 'assets/flutter_assets/AssetManifest.bin']:
                archive.writestr(name, b'diagnostic structure fixture')
            with warnings.catch_warnings():
                warnings.filterwarnings('ignore', message="Duplicate name: 'classes.dex'", category=UserWarning)
                for name, content in extras:
                    archive.writestr(name, content)
        return apk

    def test_apk_structure_does_not_certify_build_or_signing(self):
        value = self.module.validate_apk(self.apk())
        self.assertEqual(value['entries'], 4)
        self.assertEqual(value['verification'], 'ZIP safety and diagnostic Flutter structure only')
        self.assertNotIn('signatureVerified', value)

    def test_unsafe_archive_entries_are_rejected_without_extracting(self):
        for name in ['../escape', '/absolute', 'a/../escape', 'a\\escape', 'classes.dex', 'CLASSES.DEX', 'parent', 'safe/x']:
            with self.subTest(name=name):
                extras = [(name, b'bad')]
                if name == 'parent':
                    extras = [('parent/child', b'child'), ('parent', b'file')]
                if name == 'safe/x':
                    extras = [('Safe/one', b'one'), ('safe/x', b'two')]
                with self.assertRaises(ValueError):
                    self.module.validate_apk(self.apk(extras))
        linked = zipfile.ZipInfo('link')
        linked.create_system = 3
        linked.external_attr = (stat.S_IFLNK | 0o777) << 16
        with self.assertRaises(ValueError):
            self.module.validate_apk(self.apk([(linked, b'/outside')]))
        self.assertFalse((self.root / 'escape').exists())

    def test_source_snapshot_preserves_nested_quality_and_excludes_runtime(self):
        for name in ['backend/main.go', 'client/android/com/quality/MainActivity.kt', 'Library/Application Support/rtk/history.db', 'quality/old-report', '.intelligence-artifact']:
            p = self.root / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(name)
        files = self.module.source_snapshot(self.root)
        self.assertEqual(set(files), {'backend/main.go', 'client/android/com/quality/MainActivity.kt'})
        (self.root / 'linked').symlink_to(self.root / 'backend/main.go')
        with self.assertRaises(ValueError):
            self.module.source_snapshot(self.root)

    def test_apk_declared_expansion_and_corrupt_bytes_are_rejected(self):
        apk = self.apk()
        original = apk.read_bytes()
        data = bytearray(original)
        central = data.index(b'PK\x01\x02')
        struct.pack_into('<I', data, central + 24, 101 * 1024 * 1024)
        apk.write_bytes(data)
        with self.assertRaisesRegex(ValueError, 'expansion limit'):
            self.module.validate_apk(apk)
        apk.write_bytes(original)
        with zipfile.ZipFile(apk) as archive:
            first = archive.infolist()[0]
            data = bytearray(original)
            data[first.header_offset + 30 + len(first.filename.encode()) + len(first.extra)] ^= 1
        apk.write_bytes(data)
        with self.assertRaisesRegex(ValueError, 'CRC mismatch'):
            self.module.validate_apk(apk)

    def test_installed_package_is_never_replaced_or_uninstalled(self):
        calls = []
        def device(*args):
            calls.append(args)
            return 'package:/data/app/existing/base.apk\n' if args[:3] == ('shell', 'pm', 'path') else ''
        with self.assertRaisesRegex(ValueError, 'already installed'):
            self.module.require_absent_package(device, 'com.intelligencesubstrate.quality.android_feedback')
        self.assertEqual(calls, [('shell', 'pm', 'path', 'com.intelligencesubstrate.quality.android_feedback')])

    def test_real_pm_absent_status_is_accepted_but_transport_errors_are_not(self):
        def absent(*args):
            raise subprocess.CalledProcessError(1, args, b'', b'')
        try:
            self.module.require_absent_package(absent, 'com.intelligencesubstrate.quality.android_feedback')
        except subprocess.CalledProcessError:
            self.fail('Android pm path legitimately returns status1 and empty output for an absent package')
        def disconnected(*args):
            raise subprocess.CalledProcessError(1, args, b'', b'device offline')
        with self.assertRaises(subprocess.CalledProcessError):
            self.module.require_absent_package(disconnected, 'com.intelligencesubstrate.quality.android_feedback')

    def test_install_ownership_is_recorded_before_device_dispatch(self):
        state = {'installAttempted': False}
        calls = []
        def device(*args):
            calls.append(args)
            self.assertTrue(state['installAttempted'])
            raise TimeoutError('device accepted bytes but reply was lost')
        with self.assertRaises(TimeoutError):
            self.module.install_owned_package(device, self.root / 'package.apk', state)
        self.assertTrue(state['installAttempted'])
        self.assertEqual(calls[0][0], 'install')

    def test_content_addressed_package_gets_exact_apk_install_copy(self):
        prepare = getattr(self.module, 'prepare_install_apk', None)
        self.assertTrue(callable(prepare), 'ADB requires a .apk filename for a content-addressed platform artifact')
        source = self.root / '.intelligence-artifact'
        source.write_bytes(b'exact downloaded APK bytes')
        destination = self.root / 'install.apk'
        prepare(source, destination)
        self.assertEqual(destination.read_bytes(), source.read_bytes())
        with self.assertRaises(FileExistsError):
            prepare(source, destination)

    def test_installed_digest_accepts_real_android_randomized_path(self):
        digest = getattr(self.module, 'installed_apk_digest', None)
        self.assertTrue(callable(digest), 'real Android installed paths contain a randomized ~~ prefix')
        def device(*args):
            if args[:3] == ('shell', 'pm', 'path'):
                return 'package:/data/app/~~random_+=/com.example-random==/base.apk\n'
            self.assertEqual(args, ('exec-out', 'sha256sum', '/data/app/~~random_+=/com.example-random==/base.apk'))
            return 'a' * 64 + '  /data/app/~~random_+=/com.example-random==/base.apk\n'
        self.assertEqual(digest(device, 'com.example'), 'a' * 64)

    def test_occupied_api_is_not_used_or_stopped(self):
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            listener.listen()
            with self.assertRaises(OSError):
                self.module.require_free_port(listener.getsockname()[1])
            self.assertNotEqual(listener.fileno(), -1)

    def test_concurrent_device_owner_is_refused(self):
        with self.module.device_lock('emulator-5586', 15039, self.root / 'locks'):
            with self.assertRaises(BlockingIOError):
                with self.module.device_lock('emulator-5586', 15039, self.root / 'locks'):
                    self.fail('second owner acquired the same emulator')

    def test_emulator_identity_works_without_host_console_auth(self):
        validate = getattr(self.module, 'require_emulator_identity', None)
        self.assertTrue(callable(validate), 'native Worker needs AVD identity without console credentials from the user HOME')
        calls = []
        def device(*args):
            calls.append(args)
            if args == ('shell', 'getprop', 'ro.boot.qemu.avd_name'):
                return 'IS_QA_015\n'
            if args == ('shell', 'getprop', 'sys.boot_completed'):
                return '1\n'
            return ''
        validate(device, 'IS_QA_015')
        with self.assertRaisesRegex(ValueError, 'approved booted AVD'):
            validate(device, 'another-avd')
        self.assertFalse(any(c[0] == 'emu' for c in calls))

    def test_release_signature_requires_exact_certificate_and_v2_v3_non_debuggable(self):
        validate = getattr(self.module, 'validate_signature', None)
        self.assertTrue(callable(validate), 'release smoke must independently check the exact SDK signature')
        certificate = 'a' * 64
        text = '\n'.join(['Signer #1 certificate DN: CN=Ephemeral Test Release',
            'Signer #1 certificate SHA-256 digest: ' + certificate,
            'Verified using v2 scheme (APK Signature Scheme v2): true',
            'Verified using v3 scheme (APK Signature Scheme v3): true'])
        self.assertEqual(validate(text, 'package metadata', certificate)['certificateSHA256'], certificate)
        for signature, badging, expected in [
            (text, 'application-debuggable', certificate), (text, '', 'b' * 64),
            (text.replace('CN=Ephemeral Test Release', 'CN=Android Debug'), '', certificate),
            (text.replace('v3): true', 'v3): false'), '', certificate),
            (text + '\nSigner #2 certificate SHA-256 digest: ' + certificate, '', certificate),
            (text, '', certificate.upper()),
        ]:
            with self.subTest(signature=signature, badging=badging, expected=expected), self.assertRaises(ValueError):
                validate(signature, badging, expected)
        self.assertEqual(validate('Signer #1 certificate DN: CN=Android Debug', '', None)['kind'], 'development-debug-key')
        with self.assertRaises(ValueError):
            validate(text, '', None)


if __name__ == '__main__':
    unittest.main()
