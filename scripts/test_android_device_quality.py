import importlib.util
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tarfile
import tempfile
import unittest


SCRIPT = Path(__file__).with_name('android_device_quality.py')
PROFILE = {
    'schemaVersion': 1, 'serial': 'USB_QA_123', 'adbServerPort': 15039,
    'manufacturer': 'Acme', 'model': 'QA Phone',
    'fingerprint': 'acme/qa/device:16/ID/123:user/release-keys', 'sdk': 36,
    'package': 'com.company.customer_feedback', 'versionCode': 1, 'versionName': '1.0.0',
    'certificateSHA256': 'a' * 64, 'mode': 'feedback',
    'apiURL': 'https://qa.company.example', 'dedicatedTestAPI': True,
}
PROPERTIES = {
    'ro.product.manufacturer': 'Acme', 'ro.product.model': 'QA Phone',
    'ro.build.fingerprint': 'acme/qa/device:16/ID/123:user/release-keys',
    'ro.build.version.sdk': '36', 'ro.product.cpu.abilist': 'arm64-v8a,armeabi-v7a',
    'sys.boot_completed': '1', 'ro.kernel.qemu': '', 'ro.boot.qemu': '',
    'ro.boot.qemu.avd_name': '',
}
BADGING = "package: name='com.company.customer_feedback' versionCode='1' versionName='1.0.0'\nnative-code: 'arm64-v8a'\nlaunchable-activity: name='com.company.customer_feedback.MainActivity' label='Feedback' icon=''\n"
SIGNATURE = '\n'.join([
    'Signer #1 certificate DN: CN=Test Release',
    'Signer #1 certificate SHA-256 digest: ' + 'a' * 64,
    'Verified using v2 scheme (APK Signature Scheme v2): true',
    'Verified using v3 scheme (APK Signature Scheme v3): true',
])


def device_fixture(properties=None, listing=None, package_digest=None):
    props = {**PROPERTIES, **(properties or {})}
    data = {'installed': package_digest, 'installCount': 0, 'uninstallCount': 0}
    listing = listing if listing is not None else 'List of devices attached\nUSB_QA_123 device usb:1-2 product:qa model:QA_Phone transport_id:1\n'
    def adb(*args):
        if args == ('devices', '-l'):
            return listing
        if args[:2] == ('shell', 'getprop'):
            return props.get(args[2], '') + '\n'
        if args[:3] == ('shell', 'pm', 'path'):
            return 'package:/data/app/~~abc/com.company-123/base.apk\n' if data['installed'] else ''
        if args[:2] == ('exec-out', 'sha256sum'):
            return data['installed'] + '  /data/app/~~abc/com.company-123/base.apk\n'
        if args[:1] == ('uninstall',):
            data['installed'] = None
            data['uninstallCount'] += 1
            return 'Success\n'
        if args[:1] == ('install',):
            data['installCount'] += 1
            raise AssertionError('unit admission tests must not install anything')
        if args[:3] == ('shell', 'rm', '-f'):
            return ''
        raise AssertionError('unexpected device command ' + repr(args))
    return adb, data


class AndroidDeviceAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SCRIPT.is_file(), 'missing independent Android physical-device adapter')
        spec = importlib.util.spec_from_file_location('android_device_quality', SCRIPT)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def config(self, **changes):
        path = self.root / 'profile.json'
        path.write_text(json.dumps({**PROFILE, **changes}))
        path.chmod(0o600)
        return path

    def test_profile_requires_private_regular_file_and_dedicated_test_api(self):
        path = self.config()
        self.assertEqual(self.module.load_profile(path)['package'], 'com.company.customer_feedback')
        path.chmod(0o644)
        with self.assertRaises(ValueError):
            self.module.load_profile(path)
        path.chmod(0o600)
        linked = self.root / 'linked.json'
        linked.symlink_to(path)
        with self.assertRaises(ValueError):
            self.module.load_profile(linked)
        with self.assertRaises(ValueError):
            self.module.load_profile(self.config(dedicatedTestAPI=False))
        with self.assertRaises(ValueError):
            self.module.load_profile(self.config(extraPermission='replace-existing-app'))

    def test_profile_rejects_unsafe_urls_and_ambiguous_identity(self):
        for change in [
            {'apiURL': 'http://qa.company.example'}, {'apiURL': 'https://u:p@qa.company.example'},
            {'apiURL': 'https://qa.company.example/#secret'}, {'apiURL': 'https://qa.company.example?x=1'},
            {'apiURL': 'https://qa.company.example/api'}, {'serial': 'emulator-5554'},
            {'serial': '../device'}, {'adbServerPort': True}, {'versionCode': True},
            {'certificateSHA256': 'b' * 63}, {'sdk': 0}, {'mode': 'other'},
            {'package': 'com.company;rm'}, {'mode': 'booking'},
        ]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.module.load_profile(self.config(**change))

    def test_exact_usb_profile_is_observed_without_installation(self):
        adb, state = device_fixture()
        value = self.module.physical_identity(adb, PROFILE)
        self.assertEqual(value['fingerprint'], PROPERTIES['ro.build.fingerprint'])
        self.assertEqual(value['transport'], 'usb')
        self.assertFalse(value['hardwareAttestationVerified'])
        self.assertEqual(state['installCount'], 0)

    def test_emulator_changed_profile_and_incomplete_boot_are_rejected(self):
        for props in [
            {'ro.kernel.qemu': '1'}, {'ro.boot.qemu': '1'}, {'ro.boot.qemu.avd_name': 'QA_AVD'},
            {'ro.product.manufacturer': 'Other'}, {'ro.build.fingerprint': 'another-build'},
            {'ro.build.version.sdk': '35'}, {'sys.boot_completed': '0'},
            {'ro.product.cpu.abilist': 'x86_64'},
        ]:
            with self.subTest(props=props):
                adb, state = device_fixture(properties=props)
                with self.assertRaises(ValueError):
                    self.module.physical_identity(adb, PROFILE)
                self.assertEqual(state['installCount'], 0)

    def test_non_usb_unauthorized_missing_and_duplicate_device_are_rejected(self):
        for listing in [
            'List of devices attached\n',
            'List of devices attached\nUSB_QA_123 unauthorized usb:1-2\n',
            'List of devices attached\nUSB_QA_123 offline usb:1-2\n',
            'List of devices attached\nUSB_QA_123 device product:qa\n',
            'List of devices attached\nUSB_QA_123 device usb:1-2\nUSB_QA_123 device usb:1-3\n',
        ]:
            with self.subTest(listing=listing):
                adb, state = device_fixture(listing=listing)
                with self.assertRaises(ValueError):
                    self.module.physical_identity(adb, PROFILE)
                self.assertEqual(state['installCount'], 0)

    def test_signed_package_requires_approved_version_and_certificate(self):
        value = self.module.validate_release(BADGING, SIGNATURE, PROFILE)
        self.assertEqual(value['versionCode'], 1)
        self.assertEqual(value['activity'], 'com.company.customer_feedback.MainActivity')
        for badging, signature in [
            (BADGING.replace("versionCode='1'", "versionCode='2'"), SIGNATURE),
            (BADGING.replace("versionName='1.0.0'", "versionName='2.0.0'"), SIGNATURE),
            (BADGING.replace('com.company.customer_feedback', 'com.other.app'), SIGNATURE),
            (BADGING + 'application-debuggable\n', SIGNATURE),
            (BADGING, SIGNATURE.replace('a' * 64, 'b' * 64)),
            (BADGING, SIGNATURE.replace('CN=Test Release', 'CN=Android Debug')),
            (BADGING, SIGNATURE.replace('v3): true', 'v3): false')),
            (BADGING.replace('MainActivity', 'OtherActivity'), SIGNATURE),
        ]:
            with self.subTest(badging=badging, signature=signature), self.assertRaises(ValueError):
                self.module.validate_release(badging, signature, PROFILE)

    def test_preexisting_package_is_retained_by_admission_and_cleanup(self):
        adb, data = device_fixture(package_digest='b' * 64)
        with self.assertRaises(ValueError):
            self.module.require_absent_package(adb, PROFILE['package'])
        result = self.module.cleanup_owned(adb, PROFILE, {'initiallyAbsent': False, 'installAttempted': False}, 'a' * 64)
        self.assertFalse(result['packageRemoved'])
        self.assertEqual(data['installed'], 'b' * 64)
        self.assertEqual(data['uninstallCount'], 0)

    def test_uncertain_install_removes_only_exact_initially_absent_bytes(self):
        adb, data = device_fixture(package_digest='a' * 64)
        result = self.module.cleanup_owned(adb, PROFILE, {'initiallyAbsent': True, 'installAttempted': True}, 'a' * 64)
        self.assertTrue(result['packageRemoved'])
        self.assertIsNone(data['installed'])
        self.assertEqual(data['uninstallCount'], 1)

    def test_changed_package_or_device_is_retained_for_reconciliation(self):
        for props, installed in [({}, 'b' * 64), ({'ro.build.fingerprint': 'different'}, 'a' * 64)]:
            with self.subTest(props=props, installed=installed):
                adb, data = device_fixture(properties=props, package_digest=installed)
                with self.assertRaises(ValueError):
                    self.module.cleanup_owned(adb, PROFILE, {'initiallyAbsent': True, 'installAttempted': True}, 'a' * 64)
                self.assertEqual(data['installed'], installed)
                self.assertEqual(data['uninstallCount'], 0)

    def test_report_is_private_readable_and_never_replaces_old_output(self):
        diagnostics = self.root / 'diagnostics'
        diagnostics.mkdir()
        (diagnostics / 'result.json').write_text(json.dumps({'passed': False, 'physicalDeviceVerified': False}))
        output = self.root / 'quality.tar.gz'
        self.module.publish_report(diagnostics, output)
        first = output.read_bytes()
        self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)
        with tarfile.open(output) as archive:
            self.assertEqual(archive.getnames(), ['result.json'])
            self.assertFalse(json.load(archive.extractfile('result.json'))['passed'])
        with self.assertRaises(FileExistsError):
            self.module.publish_report(diagnostics, output)
        self.assertEqual(output.read_bytes(), first)

    def test_native_ui_ignores_controls_from_another_application(self):
        ui_type = getattr(self.module, 'AndroidUI', None)
        self.assertTrue(callable(ui_type), 'package-scoped native UI driver is missing')
        state = {'pressed': None}
        xml = '''<?xml version="1.0"?><hierarchy>
          <node package="com.other.app" text="提交反馈" enabled="true" clickable="true" bounds="[0,0][10,10]"/>
          <node package="com.company.customer_feedback" text="提交反馈" enabled="true" clickable="true" bounds="[10,20][110,120]"/>
        </hierarchy>'''
        def adb(*args):
            if args[:3] == ('shell', 'uiautomator', 'dump'):
                return ''
            if args[:2] == ('exec-out', 'cat'):
                return xml
            if args[:3] == ('shell', 'input', 'tap'):
                state['pressed'] = 'approved' if args[3:] == ('60', '70') else 'other-app'
                return ''
            raise AssertionError(args)
        ui = ui_type(adb, PROFILE, self.root, '/sdcard/is-qa-test.xml', timeout=0.05)
        ui.tap('提交反馈')
        self.assertEqual(state['pressed'], 'approved')

    def test_native_ui_cannot_satisfy_a_check_with_another_package(self):
        ui_type = getattr(self.module, 'AndroidUI', None)
        self.assertTrue(callable(ui_type), 'package-scoped native UI driver is missing')
        xml = '<?xml version="1.0"?><hierarchy><node package="com.other.app" text="expected receipt" enabled="true"/></hierarchy>'
        def adb(*args):
            if args[:3] == ('shell', 'uiautomator', 'dump'):
                return ''
            if args[:2] == ('exec-out', 'cat'):
                return xml
            raise AssertionError(args)
        ui = ui_type(adb, PROFILE, self.root, '/sdcard/is-qa-test.xml', timeout=0.05)
        with self.assertRaises(AssertionError):
            ui.find('expected receipt')


if __name__ == '__main__':
    unittest.main()
