import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tarfile
import tempfile
import unittest
import zipfile

import test_android_device_business as support
from test_android_device_quality import PROFILE
from android_quality_common import tool_command


REPO = Path(__file__).resolve().parents[1]
ENTRY = REPO / 'scripts/verify-reference-android-device.py'


class AndroidDeviceCLITests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        support.AndroidDeviceBusinessTests.setUpClass.__func__(cls)

    @classmethod
    def tearDownClass(cls):
        support.AndroidDeviceBusinessTests.tearDownClass.__func__(cls)

    def run_adapter(self, mode='feedback', scenario='normal'):
        self.assertTrue(ENTRY.is_file(), 'trusted Worker Android device CLI is missing')
        destination = self.root / (self.id().split('.')[-1] + '-' + mode)
        destination.mkdir()
        application = destination / 'app'
        application.mkdir()
        app_name = 'customer_feedback' if mode == 'feedback' else 'service_booking'
        package = 'com.company.' + app_name
        (application / 'application.json').write_text(json.dumps({'blueprint': 'flutter-go-v1', 'mode': mode,
            'name': app_name, 'organization': 'com.company'}))
        (application / 'main.dart').write_text('fixture source; not a native build')
        apk = destination / 'platform-artifact'
        with zipfile.ZipFile(apk, 'w') as archive:
            for name in ['AndroidManifest.xml', 'classes.dex', 'lib/arm64-v8a/libflutter.so', 'assets/flutter_assets/AssetManifest.bin']:
                archive.writestr(name, b'protocol fixture; not a compiled or signed APK')
        apk_sha = hashlib.sha256(apk.read_bytes()).hexdigest()
        state = destination / 'device.json'
        state.write_text(json.dumps({'installed': 'b' * 64 if scenario == 'preinstalled' else None,
            'installCount': 0, 'uninstallCount': 0, 'fields': {}, 'message': '', 'items': [], 'foreground': False}))
        with support.ReferenceHTTPS(destination / 'api', self.binary, self.cert, self.key, mode) as server, socket.socket() as adb_listener:
            adb_listener.bind(('127.0.0.1', 0)); adb_listener.listen(8)
            profile = {**PROFILE, 'mode': mode, 'package': package, 'apiURL': server.url,
                'apiCAFile': str(self.cert), 'adbServerPort': adb_listener.getsockname()[1]}
            if mode == 'booking':
                profile['slotId'] = 'first'
            profile_file = destination / 'profile.json'
            profile_file.write_text(json.dumps(profile)); profile_file.chmod(0o600)
            control = destination / 'control.json'
            control.write_text(json.dumps({'state': str(state), 'profile': profile, 'scenario': scenario,
                'apkSHA256': apk_sha, 'certificate': str(self.cert)}))
            tools = {}
            fixture = REPO / 'scripts/testdata/android_device_cli_fixture.py'
            for role in ['adb', 'aapt', 'apksigner']:
                wrapper = destination / role
                wrapper.write_text('#!' + sys.executable + '\nimport os,sys\nos.execv(sys.executable, ' +
                    repr([sys.executable, str(fixture), role, str(control)]) + ' + sys.argv[1:])\n')
                wrapper.chmod(0o700); tools[role] = wrapper
            output = destination / 'report.tar.gz'
            if scenario == 'old-report':
                output.write_bytes(b'previous immutable report')
            result = subprocess.run(tool_command([sys.executable, str(ENTRY), '--config', str(profile_file),
                '--application', str(application), '--adb', str(tools['adb']), '--aapt', str(tools['aapt']),
                '--apksigner', str(tools['apksigner']), '--output', str(output)]), capture_output=True,
                env={**os.environ, 'IS_ARTIFACT_PATH': str(apk)}, timeout=90)
            request_count = len(server.requests)
        data = json.loads(state.read_text())
        if scenario == 'old-report':
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(output.read_bytes(), b'previous immutable report')
            return data, None
        self.assertTrue(output.is_file(), result.stderr.decode() + result.stdout.decode())
        with tarfile.open(output) as archive:
            names = archive.getnames()
            self.assertEqual(len(names), len(set(names)))
            self.assertTrue(all('/' not in name and '..' not in name and archive.getmember(name).isfile() for name in names))
            report = json.load(archive.extractfile('result.json'))
        self.assertEqual(report['archive']['sha256'], apk_sha)
        self.assertEqual(report['sourceFiles']['main.dart'], hashlib.sha256(b'fixture source; not a native build').hexdigest())
        self.assertGreater(request_count, 0)
        if scenario == 'normal':
            self.assertEqual(result.returncode, 0, report.get('error'))
            self.assertTrue(report['passed'])
            self.assertEqual(report['installedAPKDigest'], apk_sha)
            self.assertEqual(report['method'], 'device')
            self.assertEqual(report['verificationEnvironment'], 'android-device')
            self.assertTrue(report['businessVerified'])
            self.assertFalse(report['backendRestartVerified'])
            self.assertFalse(report['enterpriseIdentityVerified'])
            self.assertIsNone(data['installed'])
            self.assertEqual((data['installCount'], data['uninstallCount']), (1, 1))
            self.assertIn('final.png', names)
        else:
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(report['passed'])
        return data, report

    def test_feedback_actual_cli_tls_business_report_and_owned_cleanup(self):
        self.run_adapter()

    def test_booking_actual_cli_tls_business_report_and_owned_cleanup(self):
        self.run_adapter('booking')

    def test_preinstalled_app_is_not_replaced_or_removed(self):
        data, report = self.run_adapter(scenario='preinstalled')
        self.assertEqual(data['installed'], 'b' * 64)
        self.assertEqual((data['installCount'], data['uninstallCount']), (0, 0))

    def test_lost_install_reply_is_reconciled_only_for_owned_bytes(self):
        data, report = self.run_adapter(scenario='lost-install')
        self.assertIsNone(data['installed'])
        self.assertEqual((data['installCount'], data['uninstallCount']), (1, 1))

    def test_changed_installed_bytes_are_preserved(self):
        data, report = self.run_adapter(scenario='changed-install')
        self.assertEqual(data['installed'], 'b' * 64)
        self.assertEqual(data['uninstallCount'], 0)

    def test_report_path_is_never_overwritten(self):
        data, report = self.run_adapter(scenario='old-report')
        self.assertEqual((data['installCount'], data['uninstallCount']), (0, 0))


if __name__ == '__main__':
    unittest.main()
