"""Contract fixtures; no real Developer ID identity or Apple account is used."""
import copy
from concurrent.futures import ThreadPoolExecutor
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import shutil
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import unittest

MODULE_PATH = Path(__file__).with_name('macos_signer.py')
signer = None
if MODULE_PATH.exists():
    spec = importlib.util.spec_from_file_location('macos_signer', MODULE_PATH)
    signer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(signer)

NOTARY = '11111111-2222-4333-8444-555555555555'
TEAM = 'ABCD123456'


def archive(path, extra=(), info=None):
    info = info or {'CFBundlePackageType': 'APPL', 'CFBundleIdentifier': 'com.example.native',
                    'CFBundleExecutable': 'Native', 'CFBundleShortVersionString': '1.0.0',
                    'CFBundleVersion': '1', 'CFBundleSupportedPlatforms': ['MacOSX']}
    members = [('Native.app/Contents/Info.plist', plistlib.dumps(info), 'file'),
               ('Native.app/Contents/MacOS/Native', b'\xcf\xfa\xed\xfe' + b'\0' * 60, 'exec'),
               ('Native.app/Contents/Resources/message.txt', b'unchanged resource', 'file'), *extra]
    with tarfile.open(path, 'w:gz', format=tarfile.PAX_FORMAT) as tar:
        for name, body, kind in members:
            entry = tarfile.TarInfo(name)
            entry.mode = 0o755 if kind == 'exec' else 0o644
            if kind == 'link': entry.type, entry.linkname = tarfile.SYMTYPE, body
            elif kind == 'hardlink': entry.type, entry.linkname = tarfile.LNKTYPE, body
            elif kind == 'fifo': entry.type = tarfile.FIFOTYPE
            elif kind == 'dir': entry.type = tarfile.DIRTYPE
            else: entry.size = len(body)
            tar.addfile(entry, io.BytesIO(body) if entry.isreg() else None)
    path.chmod(0o600)
    return path


class FixtureTools:
    def __init__(self, profile):
        self.profile = profile
        self.calls = []
        self.info_status = 'In Progress'
        self.info_id = NOTARY
        self.log_id = NOTARY
        self.log_digest = None
        self.fail_submit = False
        self.fail_staple = False
        self.alter_resource = False
        self.signed = set()
        self.stapled = set()
        self.submitted = None

    def run(self, tool, directory, *args):
        args = tuple(str(v) for v in args)
        self.calls.append((tool, args))
        if tool == 'openssl':
            cert = self.profile['applicationCertificateFile'] if 'application.der' in args else self.profile['installerCertificateFile']
            # Match the actual absolute -in path, not an application-selected identity.
            if '-in' in args: cert = args[args.index('-in') + 1]
            kind = 'Application' if Path(cert).name.startswith('application') else 'Installer'
            return 0, f'subject=\n CN=Developer ID {kind}: Example ({TEAM})\n OU={TEAM}\n', ''
        if tool == 'security':
            import base64
            body = ''.join('-----BEGIN CERTIFICATE-----\n' + base64.b64encode(Path(self.profile[k]).read_bytes()).decode() + '\n-----END CERTIFICATE-----\n' for k in ['applicationCertificateFile', 'installerCertificateFile'])
            return 0, body, ''
        if tool == 'codesign':
            path = Path(args[-1])
            if '--sign' in args:
                self.signed.add(str(path))
                if self.alter_resource:
                    (Path(directory) / 'application/Native.app/Contents/Resources/message.txt').write_bytes(b'changed')
                return 0, '', ''
            if '--extract-certificates' in args:
                prefix = args[args.index('--extract-certificates') + 1]
                Path(prefix + '0').write_bytes(Path(self.profile['applicationCertificateFile']).read_bytes())
                return 0, '', f'Authority=Developer ID Application: Example ({TEAM})\nTeamIdentifier={TEAM}\nCodeDirectory flags=0x10000(runtime)\nTimestamp=Nov 7 2026\n'
            if '--verify' in args: return 0, '', ''
            return (0, '', 'Signature=adhoc') if str(path) in self.signed else (1, '', 'code object is not signed at all')
        if tool == 'productbuild':
            Path(args[-1]).write_bytes(b'fixture signed flat package')
            return 0, '', ''
        if tool == 'pkgutil':
            sha = self.profile['expectedSigner'].upper()
            return 0, f'Status: signed by a developer certificate issued by Apple for distribution\n1. Developer ID Installer: Example ({TEAM})\nSHA256 Fingerprint:\n {sha}\n', ''
        if tool == 'spctl': return 0, '', 'accepted\nsource=Notarized Developer ID\n'
        if tool == 'notarytool':
            if args[0] == 'submit':
                self.submitted = Path(args[1])
                if self.fail_submit: return 1, '', 'unknown after upload'
                return 0, json.dumps({'id': NOTARY, 'message': 'uploaded'}), ''
            if args[0] == 'info': return 0, json.dumps({'id': self.info_id, 'status': self.info_status}), ''
            if args[0] == 'log':
                log = {'jobId': self.log_id.upper(), 'status': 'Accepted', 'sha256': self.log_digest or hashlib.sha256(self.submitted.read_bytes()).hexdigest(), 'issues': None}
                Path(args[2]).write_text(json.dumps(log))
                return 0, '', ''
        if tool == 'stapler':
            path = args[-1]
            if args[0] == 'staple':
                if self.fail_staple: return 1, '', 'interrupted'
                with Path(path).open('ab') as stream: stream.write(b' ticket')
                self.stapled.add(path)
                return 0, '', ''
            return (0, '', '') if path in self.stapled else (1, '', 'no ticket')
        raise AssertionError(('unhandled fixed tool', tool, args))


def configure_cli_tools(root, profile):
    fixture_file=root/'tool-fixture.json'
    fixture_file.write_text(json.dumps({'profile':profile}));fixture_file.chmod(0o600)
    program_template='''#!{python}
import importlib.util,json,os,pathlib,sys
assert not any(k.startswith('IS_') for k in os.environ)
sys.path.insert(0,{scripts!r})
spec=importlib.util.spec_from_file_location('fixture_tests',{module!r})
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
path=pathlib.Path({fixture!r});data=json.loads(path.read_text());tool=m.FixtureTools(data['profile'])
for key in ['info_status','info_id','log_id','log_digest','fail_submit','fail_staple','alter_resource']:
 if key in data:setattr(tool,key,data[key])
tool.calls=data.get('calls',[]);tool.signed=set(data.get('signed',[]));tool.stapled=set(data.get('stapled',[]))
if data.get('submitted'):tool.submitted=pathlib.Path(data['submitted'])
code,out,err=tool.run({kind!r},os.getcwd(),*sys.argv[1:])
data.update(calls=tool.calls,signed=sorted(tool.signed),stapled=sorted(tool.stapled),submitted=str(tool.submitted or ''))
path.write_text(json.dumps(data));sys.stdout.write(out);sys.stderr.write(err);sys.exit(code)
'''

    for kind in profile['tools']:
        tool=root/('fixed-'+kind)
        tool.write_text(program_template.format(python=sys.executable,scripts=str(Path(__file__).parent.resolve()),module=str(Path(__file__).resolve()),fixture=str(fixture_file),kind=kind))
        tool.chmod(0o700);profile['tools'][kind]=str(tool)
    fixture_file.write_text(json.dumps({'profile':profile}))
    return fixture_file


class MacOSSignerTests(unittest.TestCase):
    def setUp(self):
        self.assertIsNotNone(signer, 'macOS fixed signer is not implemented')
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.root.chmod(0o700)
        for name, body in [('application.der', b'fixture application certificate'), ('installer.der', b'fixture installer certificate'), ('signing.keychain-db', b'fixture private keychain'), ('entitlements.plist', plistlib.dumps({'com.apple.security.network.client': True}))]:
            path = self.root / name; path.write_bytes(body); path.chmod(0o600)
        profile = {'projectId': 'project_test', 'goalId': 'goal_test', 'target': 'macos', 'bundleId': 'com.example.native', 'expectedTeamId': TEAM,
                   'expectedSigner': hashlib.sha256((self.root/'installer.der').read_bytes()).hexdigest(),
                   'expectedApplicationSigner': hashlib.sha256((self.root/'application.der').read_bytes()).hexdigest(),
                   'applicationCertificateFile': str(self.root/'application.der'), 'installerCertificateFile': str(self.root/'installer.der'),
                   'keychainFile': str(self.root/'signing.keychain-db'), 'entitlementsFile': str(self.root/'entitlements.plist'), 'notaryProfile': 'fixed-notary',
                   'tools': {k: '/usr/bin/true' for k in ['codesign','productbuild','notarytool','stapler','pkgutil','spctl','security','openssl']}}
        self.config = {'stateDir': str(self.root/'state'), 'profiles': [profile]}
        self.profile = profile
        self.config_path = self.root/'config.json'; self.config_path.write_text(json.dumps(self.config)); self.config_path.chmod(0o600)
        self.input = archive(self.root/'unsigned.tar.gz')
        self.request = {'protocolVersion': 'macos-1', 'action': 'sign', 'operationKey': 'sign-task_test', 'projectId': 'project_test', 'goalId': 'goal_test',
                        'taskId': 'task_test', 'planId': 'plan_test', 'requirementRevision': 1, 'signingInputDigest': 'b'*64, 'sourceBindingDigest': 'c'*64,
                        'runId': 'run_first', 'repositoryId': 'repository_test', 'commit': 'd'*40, 'target': 'macos', 'artifactId': 'artifact_test',
                        'artifactDigest': hashlib.sha256(self.input.read_bytes()).hexdigest(), 'artifactSize': self.input.stat().st_size,
                        'artifactPath': str(self.input), 'expectedSigner': profile['expectedSigner'], 'expectedApplicationSigner': profile['expectedApplicationSigner'], 'expectedTeamId': TEAM}
        self.tools = FixtureTools(profile)

    def engine(self): return signer.Signer(signer.load_config(self.config_path), tools=self.tools)

    def status(self, receipt):
        request = dict(self.request, action='status', runId='run_status', artifactPath='')
        request.update({k: receipt[k] for k in ['notarizationId','requestBinding','payloadDigest']})
        return request

    def test_known_wait_reopens_without_input_and_submits_once(self):
        first = self.engine().handle(self.request)
        self.assertEqual(first['state'], 'processing')
        self.assertEqual(first['notarizationId'], NOTARY)
        self.input.unlink()
        status = self.status(first)
        self.assertEqual(self.engine().handle(status), first)
        self.tools.info_status = 'Accepted'
        final = self.engine().handle(status)
        self.assertEqual(final['state'], 'complete')
        self.assertTrue(final['notarized'])
        self.assertEqual(hashlib.sha256(Path(final['artifactPath']).read_bytes()).hexdigest(), final['artifactDigest'])
        before = list(self.tools.calls)
        self.assertEqual(self.engine().handle(status), final)
        self.assertEqual(self.tools.calls, before, 'successful replay should only read durable bytes')
        self.assertEqual(sum(k=='notarytool' and a[0]=='submit' for k,a in self.tools.calls),1)
        self.assertEqual(sum(k=='productbuild' for k,a in self.tools.calls),1)
        self.assertFalse(any(k=='security' and ('list-keychains' in a or 'default-keychain' in a) for k,a in self.tools.calls))
        inspected=self.engine().inspect('sign-task_test')
        self.assertEqual(inspected['status'],'signed')
        self.assertNotIn('keychainFile',json.dumps(inspected))

    def test_unknown_submit_and_signing_intent_never_repeat(self):
        self.tools.fail_submit=True
        with self.assertRaises(ValueError): self.engine().handle(self.request)
        before=list(self.tools.calls)
        with self.assertRaises(ValueError): self.engine().handle(self.request)
        self.assertEqual(self.tools.calls,before)
        self.assertEqual(self.engine().inspect('sign-task_test')['status'],'notary_submit_intent')

    def test_status_cannot_select_another_operation_or_configuration(self):
        first=self.engine().handle(self.request)
        for field,value in [('notarizationId','99999999-2222-4333-8444-555555555555'),('payloadDigest','e'*64),('requestBinding','e'*64),('commit','e'*40),('taskId','task_other')]:
            with self.subTest(field=field):
                request=self.status(first);request[field]=value
                with self.assertRaises(ValueError):self.engine().handle(request)
        self.profile['notaryProfile']='changed'
        self.config_path.write_text(json.dumps(self.config))
        with self.assertRaises(ValueError):self.engine().handle(self.status(first))

    def test_accepted_status_requires_exact_job_and_original_package_digest(self):
        first=self.engine().handle(self.request);self.tools.info_status='Accepted'
        for field,value in [('info_id','99999999-2222-4333-8444-555555555555'),('log_id','99999999-2222-4333-8444-555555555555'),('log_digest','e'*64)]:
            with self.subTest(field=field):
                old=getattr(self.tools,field);setattr(self.tools,field,value)
                with self.assertRaises(ValueError):self.engine().handle(self.status(first))
                self.assertFalse(any(k=='stapler' for k,a in self.tools.calls))
                setattr(self.tools,field,old)

    def test_staple_interruption_does_not_resubmit_or_restaple(self):
        first=self.engine().handle(self.request);self.tools.info_status='Accepted';self.tools.fail_staple=True
        with self.assertRaises(ValueError):self.engine().handle(self.status(first))
        before=len(self.tools.calls)
        self.tools.fail_staple=False
        with self.assertRaises(ValueError):self.engine().handle(self.status(first))
        self.assertFalse(any((k=='notarytool' and a[0]=='submit') or (k=='stapler' and a[0]=='staple') for k,a in self.tools.calls[before:]))

    def test_staple_recovery_requires_the_original_accepted_log(self):
        first=self.engine().handle(self.request);self.tools.info_status='Accepted';self.tools.fail_staple=True
        with self.assertRaises(ValueError):self.engine().handle(self.status(first))
        log=next(Path(self.config['stateDir']).glob('op-*/notary-log-*.json'))
        log.write_text(json.dumps({'jobId':NOTARY,'status':'Accepted','sha256':'e'*64,'issues':None}))
        final=log.parent/'stapled.pkg';self.tools.stapled.add(str(final))
        with self.assertRaises(ValueError):self.engine().handle(self.status(first))

    def test_archive_expansion_is_bounded_before_extracting_oversized_member(self):
        entry=tarfile.TarInfo('Native.app/oversized');entry.size=signer.MAX_EXPANDED+1
        self.input.write_bytes(entry.tobuf()+b'\0'*1024)
        with self.assertRaises(ValueError):signer.inspect_archive(self.input,self.root/'oversized')

    def test_invalid_tar_is_a_controlled_rejection(self):
        self.input.write_bytes(b'not an archive')
        with self.assertRaises(ValueError):signer.inspect_archive(self.input,self.root/'invalid')

    def test_private_state_copy_resumes_known_status_without_sign(self):
        first=self.engine().handle(self.request)
        state=Path(self.config['stateDir']);snapshot=self.root/'snapshot'
        shutil.copytree(state,snapshot);shutil.rmtree(state);shutil.copytree(snapshot,state)
        before=len(self.tools.calls);self.tools.info_status='Accepted'
        self.assertEqual(self.engine().handle(self.status(first))['state'],'complete')
        self.assertFalse(any(k in {'codesign','productbuild','security'} or (k=='notarytool' and a[0]=='submit') for k,a in self.tools.calls[before:]))

    def test_signing_must_preserve_ordinary_resources(self):
        self.tools.alter_resource=True
        with self.assertRaises(ValueError):self.engine().handle(self.request)
        self.assertFalse(any(k=='notarytool' for k,a in self.tools.calls))

    def test_archive_preserves_safe_framework_links(self):
        extra=[('Native.app/Contents/Frameworks/Example.framework/Versions/A/Example',b'\xcf\xfa\xed\xfe'+b'\0'*60,'exec'),
               ('Native.app/Contents/Frameworks/Example.framework/Versions/Current','A','link'),
               ('Native.app/Contents/Frameworks/Example.framework/Example','Versions/Current/Example','link')]
        archive(self.input,extra)
        result=signer.inspect_archive(self.input,self.root/'extract')
        self.assertEqual(result['bundleId'],'com.example.native')
        self.assertEqual((self.root/'extract/Native.app/Contents/Frameworks/Example.framework/Example').read_bytes(),b'\xcf\xfa\xed\xfe'+b'\0'*60)
        self.assertEqual(len(result['payloadDigest']),64)

    def test_verified_embedded_framework_signature_can_be_replaced_with_pinned_identity(self):
        extra=[('Native.app/Contents/Frameworks/Example.framework/Example',b'\xcf\xfa\xed\xfe'+b'\0'*60,'exec'),
               ('Native.app/Contents/Frameworks/Example.framework/_CodeSignature/CodeResources',b'existing framework seal','file')]
        archive(self.input,extra)
        self.request.update(artifactDigest=hashlib.sha256(self.input.read_bytes()).hexdigest(),artifactSize=self.input.stat().st_size)
        opdir=Path(self.config['stateDir'])/('op-'+hashlib.sha256(self.request['operationKey'].encode()).hexdigest())
        self.tools.signed.add(str(opdir/'application/Native.app/Contents/Frameworks/Example.framework/Example'))
        self.assertEqual(self.engine().handle(self.request)['state'],'processing')
        calls=self.tools.calls
        self.assertTrue(any(k=='codesign' and '--verify' in a and a[-1].endswith('Example.framework/Example') for k,a in calls))

    def test_archive_rejects_escapes_aliases_links_and_special_files(self):
        cases=[('../escaped',b'x','file'),('/absolute',b'x','file'),('Other.app/file',b'x','file'),
               ('Native.app/Contents/Resources/message.txt',b'duplicate','file'),('native.app/other',b'x','file'),
               ('Native.app/Contents/Resources/Message.txt',b'case alias','file'),
               ('Native.app/Contents/Resources/escape','../../../../escaped','link'),
               ('Native.app/Contents/Resources/absolute','/etc/passwd','link'),
               ('Native.app/Contents/Resources/loop','loop','link'),
               ('Native.app/Contents/Resources/hard','message.txt','hardlink'),
               ('Native.app/Contents/Resources/pipe',b'','fifo'),
               ('Native.app/Contents/MacOS/Native/child',b'x','file')]
        for i,entry in enumerate(cases):
            with self.subTest(entry=entry[0]):
                archive(self.input,[entry])
                with self.assertRaises(ValueError):signer.inspect_archive(self.input,self.root/f'bad{i}')
        self.assertFalse((self.root/'escaped').exists())

    def test_bundle_metadata_and_unsigned_content_are_required(self):
        base=plistlib.loads(plistlib.dumps({'CFBundlePackageType':'APPL','CFBundleIdentifier':'com.example.native','CFBundleExecutable':'Native','CFBundleShortVersionString':'1.0.0','CFBundleVersion':'1','CFBundleSupportedPlatforms':['MacOSX']}))
        for i,(key,value) in enumerate([('CFBundlePackageType','FMWK'),('CFBundleExecutable','../Native'),('CFBundleSupportedPlatforms',['iPhoneOS']),('CFBundleVersion',''),('CFBundleIdentifier','invalid')]):
            with self.subTest(key=key):
                info=dict(base);info[key]=value;archive(self.input,info=info)
                with self.assertRaises(ValueError):signer.inspect_archive(self.input,self.root/f'info{i}')
        archive(self.input,[('Native.app/Contents/_CodeSignature/CodeResources',b'old signature','file')])
        with self.assertRaises(ValueError):signer.inspect_archive(self.input,self.root/'signed-input')

    def test_requests_and_private_configuration_reject_credential_selection(self):
        for field,value in [('expectedSigner','e'*64),('expectedTeamId','OTHER12345'),('keychainFile','/tmp/foreign')]:
            with self.subTest(field=field):
                request=dict(self.request);request[field]=value
                with self.assertRaises(ValueError):self.engine().handle(request)
        with self.assertRaises(ValueError):signer.decode_request(b'{"action":"sign","ACTION":"status"}')
        self.config_path.chmod(0o644)
        with self.assertRaises(ValueError):signer.load_config(self.config_path)

    def test_debug_entitlement_and_changed_or_linked_inputs_are_rejected(self):
        for key in ['com.apple.security.get-task-allow','get-task-allow']:
            (self.root/'entitlements.plist').write_bytes(plistlib.dumps({key:True}))
            with self.assertRaises(ValueError):self.engine().handle(self.request)
        (self.root/'entitlements.plist').write_bytes(plistlib.dumps({'com.apple.security.network.client':True}))
        self.input.write_bytes(b'changed')
        with self.assertRaises(ValueError):self.engine().handle(self.request)
        real=self.root/'real.tar.gz';self.input.rename(real);self.input.symlink_to(real)
        with self.assertRaises(ValueError):self.engine().handle(self.request)
        self.assertEqual(self.tools.calls,[])

    def test_cli_fixed_subprocesses_release_and_resume_without_credentials_in_environment(self):
        if sys.platform != 'darwin': self.skipTest('native macOS CLI contract requires darwin')
        fixture_file=configure_cli_tools(self.root,self.profile)
        self.config_path.write_text(json.dumps(self.config))
        def invoke(request):
            result=subprocess.run([sys.executable,str(Path(__file__).with_name('sign-macos-pkg.py')),'--config',str(self.config_path)],input=json.dumps(request),capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(result.stderr,'')
            return json.loads(result.stdout)
        def competing():
            return subprocess.run([sys.executable,str(Path(__file__).with_name('sign-macos-pkg.py')),'--config',str(self.config_path)],input=json.dumps(self.request),capture_output=True,text=True)
        with ThreadPoolExecutor(max_workers=2) as pool:
            results=list(pool.map(lambda _:competing(),range(2)))
        accepted=[json.loads(r.stdout) for r in results if r.returncode==0]
        self.assertEqual(len(accepted),1,'one process must hold the private mutation fence')
        self.assertEqual(sorted(r.returncode for r in results),[0,1])
        self.assertFalse(any('Traceback' in r.stderr for r in results))
        first=accepted[0]
        self.assertEqual(first['state'],'processing')
        self.input.unlink()
        record=json.loads(fixture_file.read_text());record['info_status']='Accepted';fixture_file.write_text(json.dumps(record))
        final=invoke(self.status(first))
        self.assertTrue(final['notarized'])
        self.assertEqual(invoke(self.status(first)),final)
        record=json.loads(fixture_file.read_text())
        self.assertEqual(sum(k=='notarytool' and a[0]=='submit' for k,a in record['calls']),1)
        self.assertTrue(any(k=='spctl' for k,a in record['calls']))


if __name__=='__main__':unittest.main()
