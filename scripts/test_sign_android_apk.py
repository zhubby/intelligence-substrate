"""Trust-boundary fixtures; these tests do not certify an Android signature."""
import importlib.util
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import sqlite3
import stat
import struct
import sys
import tempfile
import time
import unittest
import zipfile
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('sign-android-apk.py')


class TrustedAndroidSigningTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SCRIPT.is_file(), 'administrator-owned APK signer is missing')
        spec = importlib.util.spec_from_file_location('signer', SCRIPT)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / 'state'
        self.state.mkdir(mode=0o700)
        self.keystore = self.root / 'release.p12'
        self.keystore.write_bytes(b'test fixture, not a signing identity')
        self.keystore.chmod(0o600)
        self.password = self.root / 'password'
        self.password.write_text('test-only-private-password\n')
        self.password.chmod(0o600)
        self.sdk = self.root / 'sdk'
        self.sdk.mkdir()
        tool = '''#!/usr/bin/env python3
import json, pathlib, shutil, sys, zipfile
root=pathlib.Path(__file__).parent
args=sys.argv[1:]
if pathlib.Path(__file__).name=='aapt':
 print("package: name='com.example.release' versionCode='7' versionName='1.2.3'")
 print("sdkVersion:'24'");print("native-code: 'arm64-v8a'")
 if (root/'debuggable').exists():print('application-debuggable')
 sys.exit(0)
if pathlib.Path(__file__).name=='zipalign':
 if '-c' not in args:shutil.copyfile(args[-2],args[-1])
 sys.exit(0)
if args[0]=='sign':
 with (root/'sign-calls').open('a') as f:f.write(json.dumps(args)+'\\n')
 if args[args.index('--ks-pass')+1]==args[args.index('--key-pass')+1]:
  print('password file stream reached EOF on its second use',file=sys.stderr);sys.exit(2)
 (root/'sign-entered').touch()
 if (root/'pause-sign').exists():
  import time
  deadline=time.monotonic()+10
  while (root/'pause-sign').exists() and time.monotonic()<deadline:time.sleep(.02)
 if (root/'fail-sign').exists():
  secret=pathlib.Path(args[args.index('--ks-pass')+1][5:]).read_text();print(secret,file=sys.stderr);sys.exit(7)
 output=pathlib.Path(args[args.index('--out')+1]);shutil.copyfile(args[-1],output)
 with zipfile.ZipFile(output,'a') as z:
  z.comment=b'fixture-signed'
  if (root/'mutate-payload').exists():z.writestr('extra-payload',b'not approved')
 sys.exit(0)
with zipfile.ZipFile(args[-1]) as z:signed=z.comment==b'fixture-signed'
if signed:
 print('Verified using v2 scheme (APK Signature Scheme v2): true')
 print('Verified using v3 scheme (APK Signature Scheme v3): true')
 print('Signer #1 certificate DN: CN=Synthetic Release')
 print('Signer #1 certificate SHA-256 digest: '+('b' if (root/'wrong-cert').exists() else 'a')*64)
 sys.exit(0)
print('DOES NOT VERIFY\\nERROR: Missing META-INF/MANIFEST.MF',file=sys.stderr);sys.exit(1)
'''
        for name in ['aapt', 'zipalign', 'apksigner']:
            path = self.sdk / name
            path.write_text('#!' + sys.executable + '\n' + tool.split('\n', 1)[1])
            path.chmod(0o700)
        self.config_path = self.root / 'config.json'
        self.profile = {'projectId': 'project_test', 'goalId': 'goal_test', 'target': 'android-huawei',
            'bundleId': 'com.example.release', 'expectedSigner': 'a' * 64, 'keystoreFile': str(self.keystore),
            'keyAlias': 'test-release', 'storePasswordFile': str(self.password), 'keyPasswordFile': str(self.password),
            'aaptPath': str(self.sdk / 'aapt'), 'apkSignerPath': str(self.sdk / 'apksigner'),
            'zipalignPath': str(self.sdk / 'zipalign'), 'javaHome': '/private/test-java'}
        self.write_config()
        self.apk = self.root / 'unsigned.apk'
        self.make_apk()
        self.request = {'protocolVersion': '1', 'action': 'sign', 'operationKey': 'sign-test',
            'projectId': 'project_test', 'goalId': 'goal_test', 'runId': 'run_test', 'repositoryId': 'repository_test',
            'commit': 'c' * 40, 'target': 'android-huawei', 'artifactId': 'artifact_test',
            'artifactDigest': self.module.sha(self.apk), 'artifactSize': self.apk.stat().st_size,
            'artifactPath': str(self.apk), 'expectedSigner': 'a' * 64}

    def write_config(self):
        self.config_path.write_text(json.dumps({'stateDir': str(self.state), 'profiles': [self.profile]}))
        self.config_path.chmod(0o600)

    def make_apk(self, extras=()):
        with zipfile.ZipFile(self.apk, 'w') as archive:
            for name in ['AndroidManifest.xml', 'classes.dex', 'lib/arm64-v8a/libflutter.so', 'assets/flutter_assets/AssetManifest.bin']:
                archive.writestr(name, b'structural fixture only')
            for name, value in extras:
                archive.writestr(name, value)

    def config(self):
        return self.module.load_config(self.config_path)

    def execute(self, **changes):
        return self.module.execute(self.config(), {**self.request, **changes})

    def test_unknown_duplicate_and_case_alias_protocol_fields_are_rejected(self):
        for data in [b'{"action":"sign","action":"sign"}', b'{"Action":"sign"}',
                     b'{"action":"sign","privateKey":"never"}', b'{} {}']:
            with self.subTest(data=data), self.assertRaises(ValueError):
                self.module.decode_request(data)
        self.assertEqual(self.module.decode_request(json.dumps(self.request).encode()), self.request)

    def test_profile_keys_paths_and_private_permissions_are_enforced(self):
        for key, value in [('target', 'android-google-play'), ('expectedSigner', 'Android Debug'),
                           ('bundleId', '../other'), ('apkSignerPath', 'apksigner'), ('projectId', '')]:
            with self.subTest(key=key):
                old = self.profile[key]
                self.profile[key] = value
                self.write_config()
                with self.assertRaises(ValueError):
                    self.config()
                self.profile[key] = old
        self.write_config()
        self.password.chmod(0o644)
        with self.assertRaises(ValueError):
            self.config()
        self.password.chmod(0o600)
        self.keystore.unlink()
        self.keystore.symlink_to(self.password)
        with self.assertRaises(ValueError):
            self.config()

    def test_non_string_profile_values_are_rejected_as_configuration_errors(self):
        for key in ['projectId', 'goalId', 'target']:
            with self.subTest(key=key):
                original = self.profile[key]
                self.profile[key] = ['not a scalar']
                self.write_config()
                with self.assertRaises(ValueError):
                    self.config()
                self.profile[key] = original

    def test_unsafe_apk_and_signature_entries_are_rejected_without_extraction(self):
        for name in ['../escape', '/absolute', 'a\\escape', 'CLASSES.DEX', 'META-INF/SIGNER.RSA']:
            with self.subTest(name=name):
                self.make_apk([(name, b'bad')])
                with self.assertRaises(ValueError):
                    self.module.validate_archive(self.apk)
        linked = zipfile.ZipInfo('linked')
        linked.create_system = 3
        linked.external_attr = (stat.S_IFLNK | 0o777) << 16
        self.make_apk([(linked, b'/outside')])
        with self.assertRaises(ValueError):
            self.module.validate_archive(self.apk)
        self.assertFalse((self.root / 'escape').exists())

    def test_input_hash_scope_and_debuggable_refusal_precede_signing(self):
        for changes in [{'artifactDigest': 'd' * 64}, {'artifactSize': True}, {'projectId': 'project_foreign'},
                        {'expectedSigner': 'b' * 64}, {'commit': 'short'}, {'artifactPath': str(self.password)}]:
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.execute(**changes)
        (self.sdk / 'debuggable').touch()
        with self.assertRaisesRegex(ValueError, 'release APK'):
            self.execute()
        self.assertFalse((self.sdk / 'sign-calls').exists())

    def test_exact_result_is_durable_and_replayed_without_resigning(self):
        result = self.execute()
        self.assertEqual(result['certificateSHA256'], 'a' * 64)
        self.assertEqual(result['packageName'], 'com.example.release')
        self.assertEqual(result['versionCode'], 7)
        self.assertEqual(result['artifactDigest'], self.module.sha(Path(result['artifactPath'])))
        self.assertEqual(self.module.validate_archive(self.apk)['payloadDigest'], result['payloadDigest'])
        replay = self.execute()
        self.assertEqual(replay, result)
        self.assertEqual(len((self.sdk / 'sign-calls').read_text().splitlines()), 1)
        self.assertEqual(self.module.inspect(self.config(), 'sign-test')['status'], 'signed')

    def test_one_password_source_works_with_sdk_independent_file_reads(self):
        result = self.execute()
        self.assertEqual(result['certificateSHA256'], 'a' * 64)
        args = json.loads((self.sdk / 'sign-calls').read_text().splitlines()[0])
        self.assertNotEqual(args[args.index('--ks-pass') + 1], args[args.index('--key-pass') + 1])
        self.assertNotIn('test-only-private-password', ' '.join(args))

    def test_administrator_sdk_programs_work_without_a_developer_shell_proxy(self):
        with patch.dict(os.environ, {'PATH': str(self.sdk)}):
            result = self.execute()
        self.assertEqual(result['certificateSHA256'], 'a' * 64)

    def test_same_key_cannot_change_binding_and_signed_object_tamper_cannot_replay(self):
        result = self.execute()
        with self.assertRaisesRegex(ValueError, 'binding'):
            self.execute(runId='run_other')
        Path(result['artifactPath']).write_bytes(b'tampered')
        with self.assertRaises(ValueError):
            self.execute()
        self.assertEqual(len((self.sdk / 'sign-calls').read_text().splitlines()), 1)

    def test_wrong_certificate_or_changed_payload_never_completes(self):
        for mode in ['wrong-cert', 'mutate-payload']:
            with self.subTest(mode=mode):
                (self.sdk / mode).touch()
                with self.assertRaises(ValueError):
                    self.execute(operationKey='sign-' + mode)
                self.assertNotEqual(self.module.inspect(self.config(), 'sign-' + mode)['status'], 'signed')
                (self.sdk / mode).unlink()

    def test_started_or_failed_intent_cannot_automatically_sign_again_or_leak_password(self):
        (self.sdk / 'fail-sign').touch()
        with self.assertRaises(ValueError) as error:
            self.execute()
        self.assertNotIn('test-only-private-password', str(error.exception))
        (self.sdk / 'fail-sign').unlink()
        with self.assertRaisesRegex(ValueError, 'reconciliation'):
            self.execute()
        self.assertEqual(len((self.sdk / 'sign-calls').read_text().splitlines()), 1)
        for path in self.state.rglob('*'):
            if path.is_file():
                self.assertNotIn(b'test-only-private-password', path.read_bytes())

    def test_read_only_inspect_does_not_create_state_or_invoke_sdk(self):
        with self.assertRaises(ValueError):
            self.module.inspect(self.config(), 'unknown')
        self.assertEqual(list(self.state.iterdir()), [])
        self.assertFalse((self.sdk / 'sign-calls').exists())

    def test_corrupt_or_oversized_declared_zip_entries_are_rejected(self):
        data = bytearray(self.apk.read_bytes())
        central = data.index(b'PK\x01\x02')
        struct.pack_into('<I', data, central + 24, 101 << 20)
        self.apk.write_bytes(data)
        with self.assertRaisesRegex(ValueError, 'expansion'):
            self.module.validate_archive(self.apk)
        self.make_apk()
        with zipfile.ZipFile(self.apk) as archive:
            member = archive.infolist()[0]
        data = bytearray(self.apk.read_bytes())
        data[member.header_offset + 30 + len(member.filename)] ^= 1
        self.apk.write_bytes(data)
        with self.assertRaises(zipfile.BadZipFile):
            self.module.validate_archive(self.apk)

    def test_concurrent_same_operation_has_one_signing_intent(self):
        gate = self.sdk / 'pause-sign'
        gate.touch()
        with ThreadPoolExecutor(max_workers=1) as pool:
            first = pool.submit(self.execute)
            deadline = time.monotonic() + 5
            while not (self.sdk / 'sign-entered').exists() and time.monotonic() < deadline:
                time.sleep(.02)
            try:
                self.assertTrue((self.sdk / 'sign-entered').exists())
                with self.assertRaisesRegex(ValueError, 'reconciliation'):
                    self.execute()
            finally:
                gate.unlink()
            result = first.result(timeout=10)
        self.assertEqual(result, self.execute())
        self.assertEqual(len((self.sdk / 'sign-calls').read_text().splitlines()), 1)

    def test_policy_drift_cannot_reuse_a_completed_signing_operation(self):
        self.execute()
        self.profile['javaHome'] = '/private/changed-java'
        self.write_config()
        with self.assertRaisesRegex(ValueError, 'binding'):
            self.execute()
        self.assertEqual(len((self.sdk / 'sign-calls').read_text().splitlines()), 1)


if __name__ == '__main__':
    unittest.main()
