import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest


class ScaffoldTests(unittest.TestCase):
    def fixture(self, root, fail=False):
        application = root / 'application'
        client = application / 'client'
        (client / 'lib').mkdir(parents=True)
        (client / 'test').mkdir()
        (client / 'lib/main.dart').write_text('reviewed application source')
        (client / 'test/widget_test.dart').write_text('reviewed business test')
        (application / 'application.json').write_text(json.dumps({'name': 'enterprise_app', 'organization': 'com.enterprise', 'mode': 'feedback'}))
        tools = root / 'tools'
        tools.mkdir()
        flutter = tools / 'flutter'
        flutter.write_text('#!' + sys.executable + '\n' + '''import pathlib, sys, plistlib
assert '--no-pub' in sys.argv
client = pathlib.Path(sys.argv[-1])
(client/'lib/main.dart').write_text('overwritten counter app')
(client/'test/widget_test.dart').write_text('overwritten counter test')
(client/'test/new_counter_test.dart').write_text('new counter test')
for variant in ['main','debug']:
    path=client/'android/app/src'/variant/'AndroidManifest.xml'
    path.parent.mkdir(parents=True,exist_ok=True)
    path.write_text('<manifest><application/></manifest>')
(client/'android/app/build.gradle.kts').write_text('signingConfig = signingConfigs.getByName("debug")')
for name in ['DebugProfile.entitlements','Release.entitlements']:
    path=client/'macos/Runner'/name
    path.parent.mkdir(parents=True,exist_ok=True)
    path.write_bytes(plistlib.dumps({'com.apple.security.app-sandbox':True}))
''' + ('sys.exit(7)\n' if fail else ''))
        flutter.chmod(0o700)
        return application, {**os.environ, 'PATH': str(tools) + os.pathsep + os.environ['PATH']}

    def run_script(self, application, env):
        return subprocess.run([sys.executable, str(Path(__file__).with_name('scaffold-reference-platforms.py')), '--application', str(application)], env=env, capture_output=True, text=True)

    def test_preserves_reviewed_source_and_never_uses_debug_release_signing(self):
        with tempfile.TemporaryDirectory() as folder:
            application, env = self.fixture(Path(folder))
            result = self.run_script(application, env)
            self.assertEqual(result.returncode, 0, result.stderr)
            client = application / 'client'
            self.assertEqual((client / 'lib/main.dart').read_text(), 'reviewed application source')
            self.assertEqual((client / 'test/widget_test.dart').read_text(), 'reviewed business test')
            self.assertFalse((client / 'test/new_counter_test.dart').exists())
            self.assertEqual((client / 'android/app/build.gradle.kts').read_text(), 'signingConfig = null')
            entitlement = plistlib.loads((client / 'macos/Runner/Release.entitlements').read_bytes())
            self.assertTrue(entitlement['com.apple.security.app-sandbox'])
            self.assertTrue(entitlement['com.apple.security.network.client'])
            self.assertTrue(json.loads((application / 'application.json').read_text())['nativeScaffolded'])
            self.assertNotEqual(self.run_script(application, env).returncode, 0)

    def test_partial_flutter_failure_restores_reviewed_bytes(self):
        with tempfile.TemporaryDirectory() as folder:
            application, env = self.fixture(Path(folder), fail=True)
            result = self.run_script(application, env)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual((application / 'client/lib/main.dart').read_text(), 'reviewed application source')
            self.assertEqual((application / 'client/test/widget_test.dart').read_text(), 'reviewed business test')
            self.assertNotIn('nativeScaffolded', json.loads((application / 'application.json').read_text()))


if __name__ == '__main__':
    unittest.main()
