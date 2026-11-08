"""Exercise generation without depending on a global Flutter installation."""
import os
import json
import plistlib
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET


class NewApplicationTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        source = Path(__file__).resolve().parents[1]
        (self.root / 'scripts').mkdir()
        shutil.copy(source / 'scripts/new-app.py', self.root / 'scripts/new-app.py')
        shutil.copy(source / 'scripts/build-reference-client.py', self.root / 'scripts/build-reference-client.py')
        shutil.copy(source / 'scripts/android_bundle.py', self.root / 'scripts/android_bundle.py')
        shutil.copy(source / 'scripts/reference_preview.py', self.root / 'scripts/reference_preview.py')
        shutil.copy(source / 'scripts/windows_msix.py', self.root / 'scripts/windows_msix.py')
        shutil.copytree(source / 'blueprints/flutter-go', self.root / 'blueprints/flutter-go')
        tests = self.root / 'blueprints/flutter-go/client/test'
        tests.mkdir(exist_ok=True)
        self.custom_test = "import 'package:delivery_app/main.dart';\nvoid main() { const DeliveryApp(); }\n"
        (tests / 'widget_test.dart').write_text(self.custom_test)
        (self.root / 'blueprints/flutter-go/client/pubspec.lock').write_text('packages: {}\n')
        fake_bin = self.root / 'tools'
        fake_bin.mkdir()
        flutter = fake_bin / 'flutter'
        flutter.write_text('#!' + sys.executable + '\n' + '''
import pathlib, sys
client = pathlib.Path(sys.argv[-1])
(client / 'test').mkdir(exist_ok=True)
(client / 'test/widget_test.dart').write_text('void main() { const MyApp(); }')
(client / 'lib/main.dart').write_text('void main() { }')
(client / 'pubspec.yaml').write_text('name: generated_default')
(client / 'pubspec.lock').write_text('packages: {unexpected: {}}')
(client / 'web').mkdir(exist_ok=True)
(client / 'web/flutter_bootstrap.js').write_text('scaffold-default')
(client / 'android/app/src/main').mkdir(parents=True)
(client / 'android/app/src/main/AndroidManifest.xml').write_text('<manifest xmlns:android="http://schemas.android.com/apk/res/android"><application android:label="custom_product" /></manifest>')
(client / 'android/app/src/debug').mkdir(parents=True)
(client / 'android/app/src/debug/AndroidManifest.xml').write_text('<manifest xmlns:android="http://schemas.android.com/apk/res/android"><uses-permission android:name="android.permission.INTERNET" /></manifest>')
(client / 'android/gradle/wrapper').mkdir(parents=True)
(client / 'android/gradle/wrapper/gradle-wrapper.properties').write_text('distributionUrl=https\\\\://services.gradle.org/distributions/gradle-9.3.1-all.zip\\nnetworkTimeout=10000\\n')
(client / 'android/app/build.gradle.kts').write_text('android { buildTypes { release { signingConfig = signingConfigs.getByName("debug") } } }' if not __import__('os').environ.get('NEW_APP_BAD_GRADLE') else 'unfamiliar release configuration')
(client / 'macos/Runner').mkdir(parents=True)
(client / 'macos/Runner/Release.entitlements').write_text('<?xml version="1.0"?><plist version="1.0"><dict><key>com.apple.security.app-sandbox</key><true/></dict></plist>')
''')
        flutter.chmod(0o700)
        self.env = {**os.environ, 'PATH': str(fake_bin) + os.pathsep + os.environ['PATH']}

    def generate(self, destination, *arguments):
        return subprocess.run(
            [sys.executable, str(self.root / 'scripts/new-app.py'), str(destination),
             '--name', 'custom_product', *arguments],
            env=self.env, text=True, capture_output=True,
        )

    def test_native_scaffolding_preserves_custom_client_and_tests(self):
        destination = self.root / 'generated'
        result = self.generate(destination, '--create-platforms', '--mode', 'booking')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((destination / 'client/test/widget_test.dart').read_text(), "import 'package:custom_product/main.dart';\nvoid main() { const DeliveryApp(); }\n")
        self.assertIn('name: custom_product', (destination / 'client/pubspec.yaml').read_text())
        self.assertIn("defaultValue: 'booking'", (destination / 'client/lib/main.dart').read_text())
        self.assertTrue((destination / 'client/android').is_dir())
        self.assertEqual((destination / 'client/pubspec.lock').read_text(), 'packages: {}\n')
        self.assertEqual(json.loads((destination / 'application.json').read_text())['mode'], 'booking')

    def test_scaffolding_works_without_the_developer_shell_proxy(self):
        self.env['PATH'] = str(self.root / 'tools')
        destination = self.root / 'without-shell-proxy'
        result = self.generate(destination, '--create-platforms', '--android-unsigned-release')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads((destination / 'application.json').read_text())['androidUnsignedRelease'])

    def test_refuses_to_overwrite_an_existing_application(self):
        destination = self.root / 'existing'
        destination.mkdir()
        (destination / 'preserve').write_text('user data')
        result = self.generate(destination)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((destination / 'preserve').read_text(), 'user data')
        self.assertEqual(sorted(p.name for p in destination.iterdir()), ['preserve'])

    def test_native_clients_can_reach_the_api_without_release_cleartext_exceptions(self):
        destination = self.root / 'network'
        result = self.generate(destination, '--create-platforms')
        self.assertEqual(result.returncode, 0, result.stderr)
        android_name = '{http://schemas.android.com/apk/res/android}'
        main = ET.parse(destination / 'client/android/app/src/main/AndroidManifest.xml').getroot()
        permissions = [node.get(android_name + 'name') for node in main.findall('uses-permission')]
        self.assertIn('android.permission.INTERNET', permissions)
        self.assertNotEqual(main.find('application').get(android_name + 'usesCleartextTraffic'), 'true')
        debug = ET.parse(destination / 'client/android/app/src/debug/AndroidManifest.xml').getroot()
        self.assertEqual(debug.find('application').get(android_name + 'usesCleartextTraffic'), 'true')
        with (destination / 'client/macos/Runner/Release.entitlements').open('rb') as stream:
            self.assertTrue(plistlib.load(stream)['com.apple.security.network.client'])

    def test_invalid_namespace_does_not_create_any_files(self):
        destination = self.root / 'invalid'
        result = self.generate(destination, '--organization', 'invalid/namespace')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(destination.exists())

    def test_generation_excludes_local_build_and_dependency_caches(self):
        source_client = self.root / 'blueprints/flutter-go/client'
        for folder in ['build', '.dart_tool']:
            (source_client / folder).mkdir(exist_ok=True)
            (source_client / folder / 'local-state').write_text('not portable')
        destination = self.root / 'portable'
        result = self.generate(destination)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((destination / 'client/build').exists())
        self.assertFalse((destination / 'client/.dart_tool').exists())

    def test_generated_application_carries_its_build_entry_point(self):
        destination = self.root / 'worker-source'
        result = self.generate(destination)
        self.assertEqual(result.returncode, 0, result.stderr)
        help_result = subprocess.run([sys.executable, str(destination / 'scripts/build-reference-client.py'), '--help'],
                                     cwd=destination, capture_output=True, text=True)
        self.assertEqual(help_result.returncode, 0, help_result.stderr)
        self.assertEqual((destination / 'scripts/windows_msix.py').read_bytes(),
                         (self.root / 'scripts/windows_msix.py').read_bytes())

    def test_native_scaffolding_preserves_self_hosted_web_bootstrap(self):
        destination = self.root / 'self-hosted'
        result = self.generate(destination, '--create-platforms')
        self.assertEqual(result.returncode, 0, result.stderr)
        expected = self.root / 'blueprints/flutter-go/client/web/flutter_bootstrap.js'
        self.assertEqual((destination / 'client/web/flutter_bootstrap.js').read_bytes(), expected.read_bytes())

    def test_explicit_gradle_override_keeps_other_wrapper_settings(self):
        destination = self.root / 'approved-gradle'
        result = self.generate(destination, '--create-platforms', '--gradle-version', '9.4.1')
        self.assertEqual(result.returncode, 0, result.stderr)
        values = dict(line.split('=', 1) for line in (destination / 'client/android/gradle/wrapper/gradle-wrapper.properties').read_text().splitlines() if '=' in line)
        self.assertEqual(values['networkTimeout'], '10000')
        self.assertEqual(values['distributionUrl'], 'https\\://services.gradle.org/distributions/gradle-9.4.1-bin.zip')
        self.assertEqual(values['distributionSha256Sum'], '2ab2958f2a1e51120c326cad6f385153bb11ee93b3c216c5fccebfdfbb7ec6cb')

    def test_unknown_gradle_override_creates_no_application(self):
        destination = self.root / 'unknown-gradle'
        result = self.generate(destination, '--create-platforms', '--gradle-version', 'unapproved')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(destination.exists())

    def test_macos_direct_distribution_is_explicit_and_recorded(self):
        destination = self.root / 'direct-desktop'
        result = self.generate(destination, '--create-platforms', '--macos-direct-distribution')
        self.assertEqual(result.returncode, 0, result.stderr)
        with (destination / 'client/macos/Runner/Release.entitlements').open('rb') as stream:
            entitlements = plistlib.load(stream)
        self.assertFalse(entitlements['com.apple.security.app-sandbox'])
        self.assertTrue(entitlements['com.apple.security.network.client'])
        self.assertTrue(json.loads((destination / 'application.json').read_text())['macosDirectDistribution'])

    def test_default_macos_generation_retains_the_application_sandbox(self):
        destination = self.root / 'sandboxed-desktop'
        result = self.generate(destination, '--create-platforms')
        self.assertEqual(result.returncode, 0, result.stderr)
        with (destination / 'client/macos/Runner/Release.entitlements').open('rb') as stream:
            self.assertTrue(plistlib.load(stream)['com.apple.security.app-sandbox'])

    def test_direct_distribution_requires_platform_generation_before_copying(self):
        destination = self.root / 'invalid-direct'
        result = self.generate(destination, '--macos-direct-distribution')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(destination.exists())

    def test_unsigned_android_release_requires_scaffolding_before_copy(self):
        destination = self.root / 'invalid-unsigned'
        result = self.generate(destination, '--android-unsigned-release')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(destination.exists())

    def test_unsigned_release_is_explicit_and_removes_only_release_debug_signing(self):
        destination = self.root / 'unsigned'
        result = self.generate(destination, '--create-platforms', '--android-unsigned-release')
        self.assertEqual(result.returncode, 0, result.stderr)
        gradle = (destination / 'client/android/app/build.gradle.kts').read_text()
        self.assertIn('signingConfig = null', gradle)
        self.assertNotIn('signingConfig = signingConfigs.getByName("debug")', gradle)
        self.assertTrue(json.loads((destination / 'application.json').read_text())['androidUnsignedRelease'])
        other = self.root / 'default-signing'
        result = self.generate(other, '--create-platforms')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('signingConfig = signingConfigs.getByName("debug")', (other / 'client/android/app/build.gradle.kts').read_text())

    def test_unsigned_generation_refuses_an_unrecognized_gradle_scaffold(self):
        self.env['NEW_APP_BAD_GRADLE'] = '1'
        destination = self.root / 'unfamiliar'
        result = self.generate(destination, '--create-platforms', '--android-unsigned-release')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('unrecognized Android release signing scaffold', result.stderr)
        self.assertFalse((destination / 'application.json').exists())


if __name__ == '__main__':
    unittest.main()
