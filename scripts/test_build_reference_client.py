import hashlib
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tarfile
import tempfile
import unittest


class ReferenceBuildTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.app = self.root / 'app'
        (self.app / 'client/lib').mkdir(parents=True)
        (self.app / 'client/lib/main.dart').write_text('void main() {}')
        (self.app / 'client/pubspec.yaml').write_text('name: example_app\nversion: 0.1.0+1\n')
        (self.app / 'application.json').write_text(json.dumps({'name': 'example_app', 'mode': 'booking', 'organization': 'com.example'}))
        self.log = self.root / 'commands.jsonl'
        self.flutter = self.root / 'flutter'
        self.flutter.write_text('#!' + sys.executable + '\n' + '''
import json, os, pathlib, sys
args = sys.argv[1:]
with open(os.environ['BUILD_TEST_LOG'], 'a') as f: f.write(json.dumps(args) + '\\n')
if args == ['--version', '--machine']:
 print(json.dumps({'frameworkVersion': 'fixed-test-sdk', 'frameworkRevision': 'a' * 40})); sys.exit(0)
if args[0] == os.environ.get('BUILD_TEST_FAIL'): sys.exit(7)
if args[0]=='build' and os.environ.get('BUILD_TEST_RACE_MANIFEST'):
 pathlib.Path(os.environ['BUILD_TEST_RACE_MANIFEST']).write_bytes(b'concurrent manifest')
if args[:2] == ['build', 'web']:
 p=pathlib.Path('build/web');p.mkdir(parents=True);(p/'index.html').write_text('<main>Actual archive content</main>')
if args[:2] == ['build', 'apk']:
 p=pathlib.Path('build/app/outputs/flutter-apk');p.mkdir(parents=True, exist_ok=True)
 (p/('app-release.apk' if '--release' in args else 'app-debug.apk')).write_bytes(b'test-unsigned-release' if '--release' in args else b'test-debug-package')
if args[:2] == ['build', 'appbundle']:
 import zipfile
 p=pathlib.Path('build/app/outputs/bundle/release');p.mkdir(parents=True)
 with zipfile.ZipFile(p/'app-release.aab','w') as z:
  for name in ['BundleConfig.pb','base/manifest/AndroidManifest.xml','base/dex/classes.dex','base/lib/arm64-v8a/libflutter.so','base/assets/flutter_assets/AssetManifest.bin']:z.writestr(name,b'structural fixture')
if args[:2] == ['build', 'ios']:
 import plistlib
 device='--release' in args
 p=pathlib.Path('build/ios/iphoneos/Runner.app' if device else 'build/ios/iphonesimulator/Runner.app');p.mkdir(parents=True)
 (p/'Runner').write_bytes(b'simulator executable');(p/'Runner').chmod(0o755)
 info={'CFBundleIdentifier':'com.example.exampleApp','CFBundleExecutable':'Runner','CFBundleShortVersionString':'0.1.0','CFBundleVersion':'1','CFBundleSupportedPlatforms':['iPhoneOS' if device else 'iPhoneSimulator'],'DTPlatformName':'iphoneos' if device else 'iphonesimulator'}
 if os.environ.get('BUILD_TEST_BAD_PLATFORM'): info['CFBundleSupportedPlatforms']=['iPhoneSimulator' if device else 'iPhoneOS']
 if os.environ.get('BUILD_TEST_PROFILE'): (p/'embedded.mobileprovision').write_bytes(b'foreign profile')
 (p/'Info.plist').write_bytes(plistlib.dumps(info))
''')
        self.flutter.chmod(0o700)
        self.xcrun = self.root / 'xcrun'
        self.xcrun.write_text('#!' + sys.executable + '\n' + '''
import json, os, pathlib, plistlib, sys
args=sys.argv[1:]
with open(os.environ['NATIVE_TEST_LOG'],'a') as f: f.write(json.dumps(args)+'\\n')
if os.environ.get('NATIVE_TEST_LICENSE'):
 print('You have not agreed to the Xcode license agreements.',file=sys.stderr);sys.exit(69)
if args==['xcodebuild','-version']:
 print('Xcode 99.1\\nBuild version 99A1');sys.exit(0)
if args[:1]==['--sdk']:
 print('99.1');sys.exit(0)
if args[:1]==['xcodebuild']:
 if os.environ.get('NATIVE_TEST_FAIL'): sys.exit(65)
 derived=pathlib.Path(args[args.index('-derivedDataPath')+1])
 p=derived/'Build/Products/Release/example_app.app'
 (p/'Contents/MacOS').mkdir(parents=True)
 exe=p/'Contents/MacOS/example_app';exe.write_bytes(b'macos executable');exe.chmod(0o755)
 info={'CFBundleIdentifier':'com.example.exampleApp','CFBundleExecutable':'example_app','CFBundleShortVersionString':'0.1.0','CFBundleVersion':'1','CFBundleSupportedPlatforms':['MacOSX']}
 if os.environ.get('NATIVE_TEST_BAD_EXECUTABLE'): info['CFBundleExecutable']='../../outside'
 (p/'Contents/Info.plist').write_bytes(plistlib.dumps(info))
 framework=p/'Contents/Frameworks/Example.framework';(framework/'Versions/A/Resources').mkdir(parents=True)
 (framework/'Versions/A/Example').write_bytes(b'framework binary')
 (framework/'Versions/Current').symlink_to('A')
 (framework/'Resources').symlink_to('Versions/Current/Resources')
 (framework/'Example').symlink_to('Versions/Current/Example')
 unsafe=os.environ.get('NATIVE_TEST_UNSAFE_LINK')
 if unsafe: (p/'Contents/escape').symlink_to(unsafe)
 if os.environ.get('NATIVE_TEST_FIFO'): os.mkfifo(p/'Contents/fifo')
 if os.environ.get('NATIVE_TEST_SECOND_APP'):
  (derived/'Build/Products/Release/unrelated.app').mkdir()
 sys.exit(0)
if args[:2]==['lipo','-archs']:
 print(os.environ.get('NATIVE_TEST_ARCHS','arm64 x86_64'));sys.exit(0)
if args[:1]==['codesign']:
 if os.environ.get('NATIVE_TEST_SIGNED'):
  print('Signature=adhoc',file=sys.stderr);sys.exit(0)
 if os.environ.get('NATIVE_TEST_SIGN_ERROR'):
  print('invalid object format',file=sys.stderr);sys.exit(1)
 print('test.app: code object is not signed at all',file=sys.stderr);sys.exit(1)
sys.exit(2)
''')
        self.xcrun.chmod(0o700)
        self.env = {**os.environ, 'BUILD_TEST_LOG': str(self.log)}
        self.native_log = self.root / 'native-commands.jsonl'
        self.env['NATIVE_TEST_LOG'] = str(self.native_log)
        self.script = Path(__file__).with_name('build-reference-client.py')
        self.output = self.root / 'artifacts/application.tar.gz'

    def build(self, *arguments):
        return subprocess.run([
            sys.executable, str(self.script), '--application', str(self.app),
            '--flutter', str(self.flutter), '--target', 'web', '--output', str(self.output),
            *arguments,
        ], env=self.env, text=True, capture_output=True)

    def test_requires_https_for_public_api_before_running_a_tool(self):
        result = self.build('--api-url', 'http://api.example.test', '--local-testing')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())
        self.assertFalse(self.output.exists())

    def test_generated_build_does_not_require_the_developer_shell_proxy(self):
        self.env['PATH'] = str(self.root)
        result = self.build('--api-url', 'https://api.example.test')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.output.is_file())

    def test_credentials_and_query_strings_are_not_build_inputs(self):
        for url in ['https://user:secret@api.example.test', 'https://api.example.test?token=secret']:
            with self.subTest(url=url):
                result = self.build('--api-url', url)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('secret', result.stdout + result.stderr)
                self.assertFalse(self.log.exists())

    def test_a_failed_check_stops_build_and_produces_no_artifact(self):
        self.env['BUILD_TEST_FAIL'] = 'analyze'
        result = self.build('--api-url', 'https://api.example.test')
        self.assertEqual(result.returncode, 7, result.stderr)
        commands = [json.loads(line) for line in self.log.read_text().splitlines()]
        self.assertFalse(any(command[0] == 'build' for command in commands))
        self.assertFalse(self.output.exists())

    def test_build_archive_and_manifest_describe_the_same_actual_bytes(self):
        result = self.build('--api-url', 'http://127.0.0.1:18091', '--local-testing')
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(self.output, 'r:gz') as archive:
            self.assertEqual(archive.extractfile('index.html').read(), b'<main>Actual archive content</main>')
        manifest = json.loads(Path(str(self.output) + '.json').read_text())
        self.assertEqual(manifest['artifact']['sha256'], hashlib.sha256(self.output.read_bytes()).hexdigest())
        self.assertEqual(manifest['mode'], 'booking')
        self.assertEqual(manifest['validation']['physicalDevice'], False)
        self.assertEqual(manifest['localTesting'], True)
        commands = [json.loads(line) for line in self.log.read_text().splitlines()]
        build = next(command for command in commands if command[0] == 'build')
        self.assertIn('--dart-define=APP_MODE=booking', build)
        self.assertIn('--dart-define=API_BASE_URL=http://127.0.0.1:18091', build)

    def test_preview_is_separate_profile_with_no_external_api_and_a_compiled_backend(self):
        backend = self.app / 'backend'
        backend.mkdir()
        (backend / 'main.go').write_text('package main\nfunc main() {}\n')
        go = self.root / 'go'
        go.write_text('#!' + sys.executable + '\nimport pathlib,sys\np=pathlib.Path(sys.argv[sys.argv.index("-o")+1]);p.write_bytes(b"linux-backend")\n')
        go.chmod(0o700)
        result = self.build('--target', 'web-preview', '--preview-arch', 'arm64', '--go', str(go))
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(self.output, 'r:gz') as archive:
            self.assertEqual(archive.extractfile('substrate-preview/backend').read(), b'linux-backend')
        manifest = json.loads(Path(str(self.output) + '.json').read_text())
        self.assertEqual(manifest['apiBaseURL'], '@preview')
        self.assertEqual(manifest['preview']['goarch'], 'arm64')
        self.assertEqual(manifest['validation']['browser'], False)

    def test_refuses_to_replace_an_existing_artifact(self):
        self.output.parent.mkdir()
        self.output.write_bytes(b'approved bytes')
        result = self.build('--api-url', 'https://api.example.test')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.output.read_bytes(), b'approved bytes')
        self.assertFalse(self.log.exists())

    def test_android_diagnostic_package_does_not_claim_release_signing(self):
        result = self.build('--target', 'android-debug', '--api-url', 'http://10.0.2.2:18110', '--local-testing')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.output.read_bytes(), b'test-debug-package')
        manifest = json.loads(Path(str(self.output) + '.json').read_text())
        self.assertEqual(manifest['signing'], 'development-debug-key')
        self.assertEqual(manifest['architecture'], 'android-arm64')
        self.assertFalse(manifest['validation']['storePublished'])

    def apple_build(self, target='macos-unsigned'):
        return self.build('--target', target, '--xcrun', str(self.xcrun),
                          '--api-url', 'https://api.example.test')

    def manifest(self):
        return json.loads(Path(str(self.output) + '.json').read_text())

    def test_unaccepted_xcode_license_stops_before_any_flutter_command(self):
        self.env['NATIVE_TEST_LICENSE'] = '1'
        result = self.apple_build('ios-simulator')
        self.assertEqual(result.returncode, 69, result.stderr)
        self.assertIn('license', result.stderr)
        self.assertFalse(self.log.exists())
        self.assertFalse(self.output.exists())
        self.assertFalse(Path(str(self.output) + '.json').exists())

    def test_unsigned_ios_device_build_records_exact_device_artifact_without_acceptance(self):
        self.env['NATIVE_TEST_ARCHS'] = 'arm64'
        result = self.apple_build('ios-unsigned')
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(self.output) as archive:
            info = plistlib.loads(archive.extractfile('Runner.app/Info.plist').read())
            self.assertEqual(info['CFBundleSupportedPlatforms'], ['iPhoneOS'])
            self.assertEqual(archive.extractfile('Runner.app/Runner').read(), b'simulator executable')
        manifest = self.manifest()
        self.assertEqual(manifest['apple']['platform'], 'iPhoneOS')
        self.assertEqual(manifest['apple']['sdk']['name'], 'iphoneos')
        self.assertEqual(manifest['apple']['architectures'], ['arm64'])
        self.assertEqual(manifest['signing'], 'unsigned-application-bundle')
        self.assertFalse(manifest['localTesting'])
        self.assertFalse(manifest['validation']['physicalDevice'])
        self.assertFalse(manifest['validation']['storePublished'])
        self.assertEqual(manifest['artifact']['sha256'], hashlib.sha256(self.output.read_bytes()).hexdigest())
        commands = [json.loads(line) for line in self.log.read_text().splitlines()]
        build = next(command for command in commands if command[:2] == ['build', 'ios'])
        self.assertIn('--release', build)
        self.assertIn('--no-codesign', build)
        self.assertNotIn('--simulator', build)

    def test_unsigned_ios_device_build_rejects_license_platform_arch_profile_and_signed_input(self):
        for name, setting in [('license', {'NATIVE_TEST_LICENSE':'1'}),
                              ('simulator', {'BUILD_TEST_BAD_PLATFORM':'1'}),
                              ('x86', {'NATIVE_TEST_ARCHS':'arm64 x86_64'}),
                              ('profile', {'BUILD_TEST_PROFILE':'1'}),
                              ('signed', {'NATIVE_TEST_SIGNED':'1'})]:
            with self.subTest(name=name):
                with tempfile.TemporaryDirectory() as directory:
                    output = Path(directory) / 'unsigned.tar.gz'
                    env = {**self.env, 'NATIVE_TEST_ARCHS':'arm64', **setting}
                    result = subprocess.run([sys.executable, str(self.script), '--application', str(self.app),
                        '--flutter', str(self.flutter), '--xcrun', str(self.xcrun), '--target', 'ios-unsigned',
                        '--api-url', 'https://api.example.test', '--output', str(output)], env=env, capture_output=True, text=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(output.exists())
                    self.assertFalse(Path(str(output)+'.json').exists())
                    if name == 'license':
                        self.assertEqual(result.returncode, 69, result.stderr)
                        self.assertFalse(self.log.exists())
                # Keep each case's native build inputs independent.
                import shutil
                shutil.rmtree(self.app / 'client/build', ignore_errors=True)

    def test_unsigned_ios_device_build_rejects_local_http_api_before_tools(self):
        result = self.build('--target', 'ios-unsigned', '--api-url', 'http://127.0.0.1:18091', '--local-testing')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())

    def test_simulator_archive_records_actual_platform_and_does_not_claim_device_acceptance(self):
        result = self.apple_build('ios-simulator')
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(self.output, 'r:gz') as archive:
            self.assertEqual(archive.extractfile('Runner.app/Runner').read(), b'simulator executable')
            self.assertEqual(archive.getmember('Runner.app/Runner').mode & 0o111, 0o111)
            info = plistlib.loads(archive.extractfile('Runner.app/Info.plist').read())
        manifest = self.manifest()
        self.assertEqual(manifest['apple']['bundleIdentifier'], info['CFBundleIdentifier'])
        self.assertEqual(manifest['apple']['sdk'], {'name': 'iphonesimulator', 'version': '99.1'})
        self.assertEqual(manifest['apple']['xcodeVersion'], 'Xcode 99.1\nBuild version 99A1')
        self.assertEqual(manifest['apple']['architectures'], ['arm64', 'x86_64'])
        self.assertEqual(manifest['signing'], 'unsigned-application-bundle')
        self.assertFalse(manifest['validation']['emulator'])
        self.assertFalse(manifest['validation']['physicalDevice'])
        self.assertFalse(manifest['validation']['storePublished'])
        commands = [json.loads(line) for line in self.log.read_text().splitlines()]
        build = next(c for c in commands if c[:2] == ['build', 'ios'])
        for flag in ['--debug', '--simulator', '--no-codesign', '--no-pub']:
            self.assertIn(flag, build)
        self.assertEqual(manifest['artifact']['sha256'], hashlib.sha256(self.output.read_bytes()).hexdigest())

    def test_macos_unsigned_archive_preserves_internal_framework_links_and_real_bytes(self):
        result = self.apple_build()
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(self.output, 'r:gz') as archive:
            self.assertEqual(archive.extractfile('example_app.app/Contents/MacOS/example_app').read(), b'macos executable')
            link = archive.getmember('example_app.app/Contents/Frameworks/Example.framework/Versions/Current')
            self.assertTrue(link.issym())
            self.assertEqual(link.linkname, 'A')
            self.assertEqual(archive.extractfile('example_app.app/Contents/Frameworks/Example.framework/Versions/A/Example').read(), b'framework binary')
        manifest = self.manifest()
        self.assertEqual(manifest['apple']['sdk']['name'], 'macosx')
        self.assertEqual(manifest['apple']['version'], '0.1.0')
        self.assertEqual(manifest['apple']['buildNumber'], '1')
        self.assertEqual(manifest['signing'], 'unsigned-application-bundle')
        self.assertFalse(manifest['validation']['nativeApplication'])
        native = [json.loads(line) for line in self.native_log.read_text().splitlines()]
        build = next(c for c in native if c[:1] == ['xcodebuild'] and 'build' in c)
        for flag in ['CODE_SIGNING_ALLOWED=NO', 'CODE_SIGNING_REQUIRED=NO', 'CODE_SIGN_IDENTITY=']:
            self.assertIn(flag, build)
        self.assertNotIn('-allowProvisioningUpdates', build)
        self.assertNotIn('DEVELOPMENT_TEAM=', ' '.join(build))

    def test_macos_native_build_failure_does_not_publish_a_partial_artifact(self):
        self.env['NATIVE_TEST_FAIL'] = '1'
        result = self.apple_build()
        self.assertEqual(result.returncode, 65, result.stderr)
        self.assertFalse(self.output.exists())
        self.assertFalse(Path(str(self.output) + '.json').exists())

    def test_iphoneos_product_cannot_masquerade_as_a_simulator_build(self):
        self.env['BUILD_TEST_BAD_PLATFORM'] = '1'
        result = self.apple_build('ios-simulator')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('platform', result.stderr.lower())
        self.assertFalse(self.output.exists())

    def test_bundle_links_must_resolve_inside_the_application(self):
        for link in ['/etc/hosts', '../../../../../outside', 'escape']:
            with self.subTest(link=link):
                self.env['NATIVE_TEST_UNSAFE_LINK'] = link
                result = self.apple_build()
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.output.exists())

    def test_bundle_special_files_are_rejected(self):
        self.env['NATIVE_TEST_FIFO'] = '1'
        result = self.apple_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())

    def test_bundle_executable_cannot_escape_the_bundle(self):
        self.env['NATIVE_TEST_BAD_EXECUTABLE'] = '1'
        result = self.apple_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('executable', result.stderr.lower())
        self.assertFalse(self.output.exists())

    def test_unexpected_signature_or_inspection_failure_is_not_recorded_as_unsigned(self):
        for setting in ['NATIVE_TEST_SIGNED', 'NATIVE_TEST_SIGN_ERROR']:
            with self.subTest(setting=setting):
                self.env.pop('NATIVE_TEST_SIGNED', None)
                self.env.pop('NATIVE_TEST_SIGN_ERROR', None)
                self.env[setting] = '1'
                result = self.apple_build()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('sign', result.stderr.lower())
                self.assertFalse(self.output.exists())

    def test_ambiguous_macos_products_are_not_arbitrarily_selected(self):
        self.env['NATIVE_TEST_SECOND_APP'] = '1'
        result = self.apple_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())

    def test_unknown_binary_architecture_is_rejected(self):
        self.env['NATIVE_TEST_ARCHS'] = 'ppc'
        result = self.apple_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('architecture', result.stderr.lower())
        self.assertFalse(self.output.exists())

    def test_concurrent_manifest_creation_preserves_other_writer_without_orphan_artifact(self):
        self.output.parent.mkdir()
        manifest = Path(str(self.output) + '.json')
        self.env['BUILD_TEST_RACE_MANIFEST'] = str(manifest)
        result = self.build('--api-url', 'https://api.example.test')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(manifest.read_bytes(), b'concurrent manifest')
        self.assertFalse(self.output.exists())

    def unsigned_build(self, extra=()):
        metadata = self.app / 'application.json'
        value = json.loads(metadata.read_text())
        value['androidUnsignedRelease'] = True
        metadata.write_text(json.dumps(value))
        tool = self.root / 'android-sdk-tool'
        tool.write_text('#!' + sys.executable + '\n' + '''
import os, sys
if sys.argv[1:3] == ['dump', 'badging']:
 print("package: name='com.example.example_app' versionCode='1' versionName='0.1.0'")
 print("sdkVersion:'24'")
 print("native-code: 'arm64-v8a'")
 if os.environ.get('UNSIGNED_DEBUGGABLE'): print('application-debuggable')
 sys.exit(0)
if os.environ.get('UNSIGNED_ALREADY_SIGNED'): sys.exit(0)
print('DOES NOT VERIFY\\nERROR: Missing META-INF/MANIFEST.MF', file=sys.stderr)
sys.exit(1)
''')
        tool.chmod(0o700)
        return self.build('--target', 'android-unsigned-apk', '--aapt', str(tool), '--apksigner', str(tool),
                          '--api-url', 'https://api.example.test', *extra)

    def test_unsigned_android_release_has_exact_bytes_and_no_signing_claim(self):
        result = self.unsigned_build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.output.read_bytes(), b'test-unsigned-release')
        manifest = self.manifest()
        self.assertEqual(manifest['signing'], 'unsigned-apk')
        self.assertEqual(manifest['android']['packageName'], 'com.example.example_app')
        self.assertEqual(manifest['android']['versionCode'], 1)
        self.assertFalse(manifest['validation']['physicalDevice'])
        commands = [json.loads(line) for line in self.log.read_text().splitlines()]
        self.assertIn('--release', next(c for c in commands if c[:2] == ['build', 'apk']))
        self.assertNotIn('--debug', next(c for c in commands if c[:2] == ['build', 'apk']))

    def test_unsigned_android_rejects_a_debuggable_or_already_signed_product(self):
        for variable in ['UNSIGNED_DEBUGGABLE', 'UNSIGNED_ALREADY_SIGNED']:
            with self.subTest(variable=variable):
                self.env.pop('UNSIGNED_DEBUGGABLE', None)
                self.env.pop('UNSIGNED_ALREADY_SIGNED', None)
                self.env[variable] = '1'
                result = self.unsigned_build()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('unsigned release APK', result.stderr)
                self.assertFalse(self.output.exists())

    def test_unsigned_android_refuses_implicit_scaffold_http_and_missing_sdk_tools(self):
        result = self.build('--target', 'android-unsigned-apk', '--api-url', 'https://api.example.test')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())
        result = self.unsigned_build(('--api-url', 'http://10.0.2.2:18110', '--local-testing'))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())


    def test_aab_build_publishes_unsigned_bundle_with_exact_provenance(self):
        application=json.loads((self.app/'application.json').read_text())
        application['androidUnsignedRelease']=True
        (self.app/'application.json').write_text(json.dumps(application))
        java=self.root/'java-home/bin';java.mkdir(parents=True)
        executable=java/'java'
        executable.write_text('#!'+sys.executable+'\n'+"""import json,sys
args=sys.argv[1:]
if '-jar' in args:
 if 'dump' in args:print('<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="com.example.example_app" android:versionCode="1" android:versionName="0.1.0"><uses-sdk android:minSdkVersion="24"/><application android:debuggable="false"/></manifest>')
else:print(json.dumps({'signed':False,'certificateSHA256':'','payloadEntries':5}))
""")
        executable.chmod(0o700)
        jar=self.root/'bundletool.jar';jar.write_bytes(b'fixed fixture')
        verifier=self.root/'VerifyAndroidBundle.java';verifier.write_text('// fixture')
        result=self.build('--target','android-unsigned-aab','--api-url','https://api.example.test',
                          '--java-home',str(java.parent),'--bundletool',str(jar),
                          '--bundletool-sha256',hashlib.sha256(jar.read_bytes()).hexdigest(),
                          '--bundle-verifier',str(verifier))
        self.assertEqual(result.returncode,0,result.stderr)
        metadata=json.loads(Path(str(self.output)+'.json').read_text())
        self.assertEqual(metadata['signing'],'unsigned-aab')
        self.assertEqual(metadata['android']['packageName'],'com.example.example_app')
        self.assertEqual(metadata['artifact']['sha256'],hashlib.sha256(self.output.read_bytes()).hexdigest())
        self.assertFalse(metadata['validation']['storePublished'])
        commands = [json.loads(line) for line in self.log.read_text().splitlines()]
        self.assertIn(['build','appbundle','--release','--target-platform=android-arm64','--no-pub','--dart-define=API_BASE_URL=https://api.example.test','--dart-define=APP_MODE=booking'],commands)


if __name__ == '__main__':
    unittest.main()
