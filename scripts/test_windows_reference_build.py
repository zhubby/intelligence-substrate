import hashlib
import json
from pathlib import Path
import subprocess
import sys
import unittest

import test_build_reference_client as reference_tests
import windows_msix_fixture as fixture


class WindowsReferenceBuildTests(unittest.TestCase):
    def setUp(self):
        self.case=reference_tests.ReferenceBuildTest();self.case.setUp();self.addCleanup(self.case.doCleanups)
        self.root=self.case.root
        self.case.env['PATHEXT']='.exe'  # Match Windows executable lookup in the cross-host fixture.
        self.assets=self.root/'assets';self.assets.mkdir()
        for name,body in fixture.entries().items():
            if name.startswith('Assets/'):(self.assets/Path(name).name).write_bytes(body)
        with self.case.flutter.open('a') as stream:
            stream.write('\n'+f'import sys;sys.path.insert(0,{str(Path(__file__).parent)!r})\n'+'''
if args[:2]==['build','windows']:
 import windows_msix_fixture as fixture
 p=pathlib.Path('build/windows/x64/runner/Release');p.mkdir(parents=True)
 (p/'example_app.exe').write_bytes(fixture.pe());(p/'flutter_windows.dll').write_bytes(fixture.pe(True))
''')
        renamed=self.case.flutter.with_suffix('.exe');self.case.flutter.rename(renamed);self.case.flutter=renamed
        self.makeappx=self.root/'makeappx.exe'
        self.makeappx.write_text('#!'+sys.executable+'\n'+f'import sys;sys.path.insert(0,{str(Path(__file__).parent)!r})\n'+'''
from pathlib import Path
import json,os
import windows_msix_fixture as fixture
args=sys.argv[1:]
assert args[0]=='pack' and '/nv' not in args and '/h' in args and args[args.index('/h')+1]=='SHA256'
root=Path(args[args.index('/d')+1]);output=Path(args[args.index('/p')+1])
if os.environ.get('WINDOWS_BUILD_FAIL'):sys.exit(11)
values={p.relative_to(root).as_posix():p.read_bytes() for p in root.rglob('*') if p.is_file()}
fixture.write(output,values,signed=bool(os.environ.get('WINDOWS_BUILD_SIGNED')))
''');self.makeappx.chmod(0o700)
        self.output=self.root/'app.msix'

    def build(self,host=True,extra=()):
        arguments=['--application',str(self.case.app),'--flutter',str(self.case.flutter),'--target','windows-unsigned-msix','--output',str(self.output),
                   '--api-url','https://api.example.test','--makeappx',str(self.makeappx),'--windows-publisher','CN=Company','--windows-package','com.company.feedback','--windows-version','1.0.0.0','--windows-assets',str(self.assets),*extra]
        if host:
            code='import sys,runpy;sys.path.insert(0,'+repr(str(Path(__file__).parent))+');sys.argv='+repr([str(self.case.script),*arguments])+';loaded=runpy.run_path(sys.argv[0],run_name="windows_build_test");sys.platform="win32";sys.exit(loaded["main"]())'
            argv=[sys.executable,'-c',code]
        else:argv=[sys.executable,str(self.case.script),*arguments]
        return subprocess.run(argv,env=self.case.env,capture_output=True,text=True)

    def test_requires_native_windows_before_any_flutter_command(self):
        result=self.build(host=False)
        self.assertNotEqual(result.returncode,0);self.assertIn('native Windows',result.stderr)
        self.assertFalse(self.case.log.exists());self.assertFalse(self.output.exists())

    def test_standard_flutter_batch_launcher_uses_pinned_dart_snapshot_without_a_shell(self):
        sdk=self.root/'flutter-sdk';bin=sdk/'bin';dart=bin/'cache/dart-sdk/bin/dart.exe';dart.parent.mkdir(parents=True)
        code=self.case.flutter.read_text().replace('args = sys.argv[1:]','args = sys.argv[1:]\nassert args[0]=="--disable-dart-dev" and args[1].startswith("--packages=") and args[2].endswith("flutter_tools.snapshot")\nargs=args[3:]')
        dart.write_text(code);dart.chmod(0o700)
        (bin/'cache/flutter_tools.snapshot').write_bytes(b'pre-provisioned SDK snapshot')
        packages=sdk/'packages/flutter_tools/.dart_tool/package_config.json';packages.parent.mkdir(parents=True);packages.write_text('{}')
        batch=bin/'flutter.bat';batch.write_text('must never execute via command shell');batch.chmod(0o700)
        self.case.flutter=batch;self.case.env['PATHEXT']='.bat:.exe'
        result=self.build();self.assertEqual(result.returncode,0,result.stderr)
        self.assertTrue(self.output.is_file());self.assertTrue(self.case.log.is_file())

    def test_exact_unsigned_msix_and_truthful_manifest(self):
        result=self.build();self.assertEqual(result.returncode,0,result.stderr)
        body=json.loads(Path(str(self.output)+'.json').read_text())
        self.assertEqual(body['signing'],'unsigned-msix');self.assertEqual(body['architecture'],'windows-x64')
        self.assertEqual(body['windows']['publisher'],'CN=Company');self.assertEqual(body['windows']['version'],'1.0.0.0')
        self.assertEqual(body['artifact']['sha256'],hashlib.sha256(self.output.read_bytes()).hexdigest())
        self.assertFalse(body['validation']['nativeApplication']);self.assertFalse(body['validation']['storePublished'])
        commands=[json.loads(line) for line in self.case.log.read_text().splitlines()]
        build=next(c for c in commands if c[:2]==['build','windows'])
        self.assertIn('--release',build);self.assertIn('--target-platform=windows-x64',build)

    def test_http_invalid_identity_and_missing_asset_stop_before_build(self):
        for args in [('--api-url','http://127.0.0.1:9000','--local-testing'),('--windows-version','1.0.0'),('--windows-package','../escape'),('--windows-publisher','CN=Other\nInjected'),('--windows-assets',str(self.root/'missing'))]:
            with self.subTest(args=args):
                result=self.build(extra=args);self.assertNotEqual(result.returncode,0)
                self.assertFalse(self.case.log.exists());self.assertFalse(self.output.exists())

    def test_makeappx_failure_or_preexisting_signature_does_not_publish_output(self):
        for variable in ['WINDOWS_BUILD_FAIL','WINDOWS_BUILD_SIGNED']:
            with self.subTest(variable=variable):
                # A fresh generated source build isolates each output tree.
                import shutil
                shutil.rmtree(self.case.app/'client/build',ignore_errors=True)
                self.case.env[variable]='1';result=self.build();self.case.env.pop(variable)
                self.assertNotEqual(result.returncode,0);self.assertFalse(self.output.exists());self.assertFalse(Path(str(self.output)+'.json').exists())


if __name__=='__main__':unittest.main()
