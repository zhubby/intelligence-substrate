from datetime import datetime, timedelta, timezone
import base64
import copy
import hashlib
import json
import os
from pathlib import Path
import plistlib
import shutil
import sqlite3
import subprocess
import tempfile
import unittest
import zipfile

import ios_signer
from macos_signer import AppleTools
from test_ios_payload import app_entries, archive

OPENSSL = '/opt/homebrew/opt/openssl@3/bin/openssl' if Path('/opt/homebrew/opt/openssl@3/bin/openssl').is_file() else shutil.which('openssl')
TEAM = 'ABCD123456'


class CodesignProvider:
    """Only Apple keychain/codesign are synthetic; CMS/x509 execute OpenSSL3."""
    def __init__(self, profile):
        self.profile, self.calls, self.signed, self.entitlements = profile, [], set(), {}
        self.fail_sign = self.bad_entitlements = self.change_resource = False

    def run(self, tool, directory, *args):
        self.calls.append((tool, tuple(map(str,args))))
        if tool == 'openssl': return AppleTools(self.profile).run(tool,directory,*args)
        if tool == 'security':
            body = Path(self.profile['certificateFile']).read_bytes()
            return 0, '-----BEGIN CERTIFICATE-----\n'+base64.b64encode(body).decode()+'\n-----END CERTIFICATE-----\n', ''
        if tool != 'codesign': raise AssertionError('unexpected external tool')
        path = str(args[-1])
        if '--sign' in args:
            if self.fail_sign: return 1, '', 'synthetic signing failure'
            self.signed.add(path)
            app = Path(path)
            if app.suffix == '.app':
                self.entitlements[path] = plistlib.loads(Path(args[args.index('--entitlements')+1]).read_bytes())
                (app/'_CodeSignature').mkdir(exist_ok=True)
                (app/'_CodeSignature/CodeResources').write_bytes(b'explicitly synthetic Apple resource seal')
                if self.change_resource: (app/'assets/content.txt').write_bytes(b'changed without authorization')
            return 0, '', ''
        if '--entitlements' in args:
            ent = copy.deepcopy(self.entitlements[path])
            if self.bad_entitlements: ent['get-task-allow'] = True
            return 0, plistlib.dumps(ent).decode(), ''
        if '--verify' in args: return 0, '', ''
        if path not in self.signed: return 1, '', 'code object is not signed at all'
        if '--extract-certificates' in args:
            prefix = str(args[args.index('--extract-certificates')+1])
            Path(prefix+'0').write_bytes(Path(self.profile['certificateFile']).read_bytes())
        return 0, '', 'Authority=Apple Distribution: Local Test (ABCD123456)\nTeamIdentifier=ABCD123456\n'


class IOSSignerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.material = tempfile.TemporaryDirectory()
        cls.certs = Path(cls.material.name)
        def run(*args):
            subprocess.run([OPENSSL,*map(str,args)],check=True,capture_output=True)
        run('req','-x509','-newkey','rsa:2048','-nodes','-days','3','-subj','/CN=Local CMS Test Root',
            '-keyout',cls.certs/'ca.key','-out',cls.certs/'ca.pem','-addext','basicConstraints=critical,CA:TRUE')
        run('req','-x509','-newkey','rsa:2048','-nodes','-days','3','-subj','/OU=ABCD123456/CN=Apple Distribution: Local Test (ABCD123456)',
            '-keyout',cls.certs/'dist.key','-out',cls.certs/'dist.pem')
        run('x509','-in',cls.certs/'dist.pem','-outform','DER','-out',cls.certs/'dist.der')

    @classmethod
    def tearDownClass(cls): cls.material.cleanup()

    def setUp(self):
        self.temporary=tempfile.TemporaryDirectory(); self.addCleanup(self.temporary.cleanup)
        self.root=Path(self.temporary.name); self.root.chmod(0o700)
        self.private=self.root/'private'; self.private.mkdir(mode=0o700)
        for name in ['ca.pem','dist.der']:
            shutil.copyfile(self.certs/name,self.private/name); (self.private/name).chmod(0o600)
        (self.private/'keychain-db').write_bytes(b'synthetic private keychain'); (self.private/'keychain-db').chmod(0o600)
        self.ent={'application-identifier':TEAM+'.com.example.app','com.apple.developer.team-identifier':TEAM,
                  'get-task-allow':False,'keychain-access-groups':[TEAM+'.com.example.app']}
        (self.private/'entitlements.plist').write_bytes(plistlib.dumps(self.ent)); (self.private/'entitlements.plist').chmod(0o600)
        now=datetime.now(timezone.utc).replace(tzinfo=None)
        self.provision={'UUID':'11111111-2222-4333-8444-555555555555','Name':'Local test profile',
            'TeamIdentifier':[TEAM],'ApplicationIdentifierPrefix':[TEAM],'Platform':['iOS'],
            'CreationDate':now-timedelta(hours=1),'ExpirationDate':now+timedelta(days=1),
            'DeveloperCertificates':[(self.private/'dist.der').read_bytes()],
            'Entitlements':{**self.ent,'keychain-access-groups':[TEAM+'.*'],'beta-reports-active':True}}
        self.write_profile()
        self.profile={'projectId':'project_test','goalId':'goal_test','target':'ios-appstore','bundleId':'com.example.app',
            'expectedSigner':hashlib.sha256((self.private/'dist.der').read_bytes()).hexdigest(),
            'expectedTeamId':TEAM,'expectedProvisioningProfile':hashlib.sha256((self.private/'profile.mobileprovision').read_bytes()).hexdigest(),
            'certificateFile':str(self.private/'dist.der'),'profileFile':str(self.private/'profile.mobileprovision'),
            'entitlementsFile':str(self.private/'entitlements.plist'),'keychainFile':str(self.private/'keychain-db'),
            'profileTrustCAFile':str(self.private/'ca.pem'),'profileTrustCASHA256':hashlib.sha256((self.private/'ca.pem').read_bytes()).hexdigest(),
            'tools':{'openssl':OPENSSL,'codesign':'/usr/bin/true','security':'/usr/bin/true'}}
        self.config_path=self.private/'config.json'
        self.config={'stateDir':str(self.root/'state'),'profiles':[self.profile]}
        self.config_path.write_text(json.dumps(self.config)); self.config_path.chmod(0o600)
        self.input=self.root/'unsigned.tar.gz'; archive(self.input,app_entries()); self.input.chmod(0o600)
        self.request={'protocolVersion':'ios-1','action':'sign','operationKey':'sign-task_test',
            'projectId':'project_test','goalId':'goal_test','taskId':'task_test','planId':'plan_test','requirementRevision':1,
            'signingInputDigest':'b'*64,'sourceBindingDigest':'c'*64,'runId':'run_test','repositoryId':'repository_test',
            'commit':'a'*40,'target':'ios-appstore','artifactId':'artifact_test',
            'artifactDigest':hashlib.sha256(self.input.read_bytes()).hexdigest(),'artifactSize':self.input.stat().st_size,
            'artifactPath':str(self.input),'expectedSigner':self.profile['expectedSigner'],'expectedTeamId':TEAM,
            'expectedProvisioningProfile':self.profile['expectedProvisioningProfile']}
        self.tools=CodesignProvider(self.profile)

    def write_profile(self):
        source=self.private/'profile.plist'; source.write_bytes(plistlib.dumps(self.provision))
        subprocess.run([OPENSSL,'cms','-sign','-binary','-nodetach','-in',str(source),'-signer',str(self.certs/'ca.pem'),
            '-inkey',str(self.certs/'ca.key'),'-outform','DER','-out',str(self.private/'profile.mobileprovision')],check=True,capture_output=True)
        (self.private/'profile.mobileprovision').chmod(0o600)

    def engine(self): return ios_signer.Signer(ios_signer.load_config(self.config_path),tools=self.tools)

    def test_ipa_has_exact_profile_resources_and_durable_readonly_replay(self):
        result=self.engine().handle(self.request)
        self.assertEqual(result['protocolVersion'],'ios-1'); self.assertEqual(result['state'],'complete')
        self.assertEqual(result['profileSHA256'],self.request['expectedProvisioningProfile'])
        self.assertEqual(result['teamId'],TEAM)
        package=Path(result['artifactPath']); self.assertEqual(result['artifactDigest'],hashlib.sha256(package.read_bytes()).hexdigest())
        with zipfile.ZipFile(package) as z:
            self.assertEqual(z.read('Payload/Runner.app/assets/content.txt'),b'approved application resource')
            self.assertEqual(z.read('Payload/Runner.app/embedded.mobileprovision'),(self.private/'profile.mobileprovision').read_bytes())
            self.assertTrue(all(name.startswith('Payload/Runner.app/') for name in z.namelist()))
        count=len(self.tools.calls); self.input.unlink()
        again=self.engine().handle({**self.request,'runId':'run_second','artifactPath':str(self.root/'gone')})
        self.assertEqual(again,result); self.assertEqual(len(self.tools.calls),count)
        self.assertEqual(self.engine().inspect('sign-task_test')['status'],'signed')

    def test_signing_intent_survives_failure_and_never_automatically_resigns(self):
        self.tools.fail_sign=True
        with self.assertRaises(ValueError): self.engine().handle(self.request)
        count=len(self.tools.calls); self.tools.fail_sign=False
        with self.assertRaises(ValueError): self.engine().handle(self.request)
        self.assertEqual(len(self.tools.calls),count)
        self.assertEqual(self.engine().inspect('sign-task_test')['status'],'signing_intent')

    def test_unapproved_profile_team_device_debug_expiration_or_certificate_is_rejected(self):
        for mode in ['team','devices','all-devices','debug','expired','certificate','wildcard-app']:
            with self.subTest(mode=mode):
                changed=copy.deepcopy(self.provision)
                if mode=='team': changed['TeamIdentifier']=['OTHER12345']
                if mode=='devices': changed['ProvisionedDevices']=['device-id']
                if mode=='all-devices': changed['ProvisionsAllDevices']=True
                if mode=='debug': changed['Entitlements']['get-task-allow']=True
                if mode=='expired': changed['ExpirationDate']=datetime.now()-timedelta(days=1)
                if mode=='certificate': changed['DeveloperCertificates']=[b'foreign-certificate']
                if mode=='wildcard-app': changed['Entitlements']['application-identifier']=TEAM+'.*'
                with self.assertRaises(ValueError): ios_signer.validate_profile(changed,self.profile,self.ent)

    def test_entitlements_cannot_exceed_the_profile_or_enable_debugging(self):
        for key,value in [('get-task-allow',True),('aps-environment','production'),('keychain-access-groups',['OTHER12345.*']),('application-identifier',TEAM+'.*')]:
            ent={**self.ent,key:value}
            with self.subTest(key=key),self.assertRaises(ValueError): ios_signer.validate_profile(self.provision,self.profile,ent)

    def test_tampered_cms_fails_before_codesign(self):
        body=bytearray((self.private/'profile.mobileprovision').read_bytes()); body[-10]^=1
        (self.private/'profile.mobileprovision').write_bytes(body)
        self.profile['expectedProvisioningProfile']=hashlib.sha256(body).hexdigest()
        self.request['expectedProvisioningProfile']=self.profile['expectedProvisioningProfile']
        self.config_path.write_text(json.dumps(self.config))
        with self.assertRaises(ValueError): self.engine().handle(self.request)
        self.assertFalse(any(tool=='codesign' and '--sign' in args for tool,args in self.tools.calls))

    def test_wrong_trust_anchor_fails_before_codesign(self):
        shutil.copyfile(self.certs/'dist.pem',self.private/'ca.pem')
        self.profile['profileTrustCASHA256']=hashlib.sha256((self.private/'ca.pem').read_bytes()).hexdigest()
        self.config_path.write_text(json.dumps(self.config))
        with self.assertRaises(ValueError): self.engine().handle(self.request)
        self.assertFalse(any(tool=='codesign' and '--sign' in args for tool,args in self.tools.calls))

    def test_actual_signed_entitlements_cannot_enable_debugging(self):
        self.tools.bad_entitlements=True
        with self.assertRaises(ValueError): self.engine().handle(self.request)
        self.assertNotEqual(self.engine().inspect('sign-task_test')['status'],'signed')

    def test_request_fields_are_strict_and_cannot_select_keys_or_tools(self):
        for extra in [{'keychainFile':'/tmp/foreign'},{'tools':{}},{'expectedTeamId':'bad'}, {'artifactSize':True}]:
            with self.subTest(extra=extra),self.assertRaises(ValueError): ios_signer.decode_request(json.dumps({**self.request,**extra}))
        body=json.dumps(self.request)
        with self.assertRaises(ValueError): ios_signer.decode_request('{"teamId":"foreign",'+body[1:])

    def test_completed_ipa_tampering_never_replays_success(self):
        result=self.engine().handle(self.request)
        Path(result['artifactPath']).write_bytes(b'changed package')
        with self.assertRaises(ValueError): self.engine().handle(self.request)

    def test_modified_resources_or_signed_entitlements_never_produce_success(self):
        self.tools.change_resource=True
        with self.assertRaises(ValueError): self.engine().handle(self.request)
        self.assertNotEqual(self.engine().inspect('sign-task_test')['status'],'signed')

    def test_profile_or_configuration_change_cannot_reuse_completed_operation(self):
        self.engine().handle(self.request)
        self.profile['bundleId']='com.example.other'; self.config_path.write_text(json.dumps(self.config))
        with self.assertRaises(ValueError): self.engine().handle(self.request)


def export_cli_fixture(root,project,goal):
    """Generate an isolated provider fixture, never a real Apple credential."""
    root=Path(root)
    IOSSignerTest.setUpClass()
    case=IOSSignerTest()
    try:
        case.setUp()
        shutil.copytree(case.root,root)
        profile=copy.deepcopy(case.profile)
        for name in ['certificateFile','profileFile','entitlementsFile','keychainFile','profileTrustCAFile']:
            profile[name]=str(root/Path(profile[name]).relative_to(case.root))
        profile['projectId'],profile['goalId']=project,goal
        module=str(Path(__file__).resolve().parent)
        config_path=root/'private/config.json'; state=root/'provider-state.json'
        for tool in ['codesign','security']:
            path=root/('fixed-'+tool)
            path.write_text('#!'+os.sys.executable+'\n'+f'''
import json, pathlib, sys
sys.path.insert(0,{module!r})
from test_ios_signer import CodesignProvider
profile=json.loads(pathlib.Path({str(config_path)!r}).read_text())['profiles'][0]
provider=CodesignProvider(profile)
state=pathlib.Path({str(state)!r})
if state.exists():
 data=json.loads(state.read_text());provider.signed=set(data['signed']);provider.entitlements=data['entitlements']
code,out,error=provider.run({tool!r},pathlib.Path.cwd(),*sys.argv[1:])
state.write_text(json.dumps({{'signed':sorted(provider.signed),'entitlements':provider.entitlements}}));state.chmod(0o600)
sys.stdout.write(out);sys.stderr.write(error);sys.exit(code)
''')
            path.chmod(0o700); profile['tools'][tool]=str(path)
        config_path.write_text(json.dumps({'stateDir':str(root/'state'),'profiles':[profile]})); config_path.chmod(0o600)
        return {'python':os.sys.executable,'program':str(Path(__file__).with_name('sign-ios-ipa.py').resolve()),
                'config':str(config_path),'source':base64.b64encode((root/'unsigned.tar.gz').read_bytes()).decode(),
                'expectedSigner':profile['expectedSigner'],'expectedProvisioningProfile':profile['expectedProvisioningProfile']}
    finally:
        case.doCleanups(); IOSSignerTest.tearDownClass()


if __name__=='__main__': unittest.main()
