"""Fixed Java/tool boundary fixtures; real cryptography is verified separately."""
from pathlib import Path
import json
import sys
import unittest
import test_sign_android_apk as apk_tests


class AABSigningTests(unittest.TestCase):
    def setUp(self):
        self.fx = apk_tests.TrustedAndroidSigningTests('runTest')
        self.fx.setUp()
        self.addCleanup(self.fx.doCleanups)
        self.module = self.fx.module
        self.profile = self.fx.profile
        for key in ['aaptPath', 'apkSignerPath', 'zipalignPath']:
            del self.profile[key]
        java = self.fx.sdk / 'java-home/bin'
        java.mkdir(parents=True)
        self.jar = self.fx.sdk / 'bundletool.jar'
        self.jar.write_bytes(b'fixed tool fixture')
        verifier = self.fx.sdk / 'VerifyAndroidBundle.java'
        verifier.write_text('// fixed verifier fixture')
        self.profile.update({'target': 'android-google-play', 'javaHome': str(java.parent),
            'bundletoolPath': str(self.jar), 'bundletoolSHA256': self.module.sha(self.jar),
            'bundleVerifierPath': str(verifier)})
        tool = '''import json,pathlib,shutil,sys,zipfile
root=pathlib.Path(__file__).parents[2];args=sys.argv[1:]
if pathlib.Path(__file__).name=='jarsigner':
 with (root/'jar-sign-calls').open('a') as f:f.write(json.dumps(args)+'\\n')
 if (root/'jar-fail').exists():print('test-only-private-password',file=sys.stderr);sys.exit(7)
 output=pathlib.Path(args[args.index('-signedjar')+1]);shutil.copyfile(args[-2],output)
 with zipfile.ZipFile(output,'a') as z:
  z.writestr('META-INF/UPLOAD.SF',b'fixture');z.writestr('META-INF/UPLOAD.RSA',b'fixture')
  if (root/'jar-payload').exists():z.writestr('base/assets/extra',b'changed')
 sys.exit(0)
if '-jar' in args:
 if 'dump' in args:print('<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="com.example.release" android:versionCode="7" android:versionName="1.2.3"><uses-sdk android:minSdkVersion="24"/><application android:debuggable="false"/></manifest>')
 sys.exit(0)
path=pathlib.Path(args[-2]);expected=args[-1]
with zipfile.ZipFile(path) as z:signed='META-INF/UPLOAD.RSA' in z.namelist()
if (expected=='unsigned' and signed) or (expected!='unsigned' and (not signed or (root/'jar-cert').exists())):sys.exit(3)
print(json.dumps({'signed':signed,'certificateSHA256':expected if signed else '', 'payloadEntries':5}))
'''
        for name in ['java', 'jarsigner']:
            p = java / name
            p.write_text('#!' + sys.executable + '\n' + tool)
            p.chmod(0o700)
        self.fx.apk = self.fx.root / 'unsigned.aab'
        import zipfile
        with zipfile.ZipFile(self.fx.apk, 'w') as z:
            for name in ['BundleConfig.pb', 'base/manifest/AndroidManifest.xml', 'base/dex/classes.dex', 'base/lib/arm64-v8a/libflutter.so', 'base/assets/flutter_assets/AssetManifest.bin']:
                z.writestr(name, b'fixture only')
        self.fx.request.update({'target': 'android-google-play', 'artifactPath': str(self.fx.apk),
                               'artifactDigest': self.module.sha(self.fx.apk), 'artifactSize': self.fx.apk.stat().st_size})
        self.fx.write_config()

    def test_aab_profile_signs_and_replays_exact_bytes_once(self):
        result = self.fx.execute()
        self.assertTrue(result['artifactPath'].endswith('.aab'))
        self.assertEqual(result, self.fx.execute())
        self.assertEqual(result['certificateSHA256'], 'a' * 64)
        self.assertEqual(len((self.fx.sdk / 'jar-sign-calls').read_text().splitlines()), 1)

    def test_aab_wrong_certificate_or_changed_payload_never_completes(self):
        for marker in ['jar-cert', 'jar-payload']:
            with self.subTest(marker=marker):
                (self.fx.sdk / marker).touch()
                with self.assertRaises(ValueError):
                    self.fx.execute(operationKey=marker)
                self.assertNotEqual(self.module.inspect(self.fx.config(), marker)['status'], 'signed')
                (self.fx.sdk / marker).unlink()

    def test_aab_failed_intent_does_not_sign_again_or_expose_password(self):
        (self.fx.sdk / 'jar-fail').touch()
        with self.assertRaises(ValueError) as caught:
            self.fx.execute()
        self.assertNotIn('test-only-private-password', str(caught.exception))
        (self.fx.sdk / 'jar-fail').unlink()
        with self.assertRaises(ValueError):
            self.fx.execute()
        self.assertEqual(len((self.fx.sdk / 'jar-sign-calls').read_text().splitlines()), 1)

    def test_aab_tools_are_pinned_and_apk_profiles_cannot_be_reused(self):
        self.jar.write_bytes(b'changed')
        with self.assertRaises(ValueError):
            self.fx.execute()
        self.assertFalse((self.fx.sdk / 'jar-sign-calls').exists())
        self.profile['target'] = 'android-huawei'
        self.fx.write_config()
        with self.assertRaises(ValueError):
            self.fx.config()


if __name__ == '__main__':
    unittest.main()
