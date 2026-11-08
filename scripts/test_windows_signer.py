from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
import zipfile
import base64

import windows_msix_fixture as fixture
import windows_signer


class WindowsProvider:
    """Only certificate/SignTool trust is synthetic; content and journal are real."""
    def __init__(self,profile,mode='ok'):
        self.profile=profile;self.mode=mode;self.calls=[]
    def privacy(self,paths):self.calls.append('privacy')
    def identity(self):
        self.calls.append('identity')
        body=Path(self.profile['certificateFile']).read_bytes()
        return {'certificateSHA256':hashlib.sha256(body).hexdigest(),'certificateSHA1':hashlib.sha1(body).hexdigest().upper(),
                'publisher':self.profile['expectedWindowsPublisher'],'hasPrivateKey':True,'codeSigning':True,'chainValid':True,'validTime':True}
    def sign(self,path,log):
        self.calls.append('sign')
        if self.mode=='fail':raise ValueError('synthetic signing failure')
        if self.mode=='content':
            with zipfile.ZipFile(path) as z:values={n:z.read(n) for n in z.namelist() if n not in {'AppxBlockMap.xml','[Content_Types].xml'}}
            values['app/data/flutter_assets/content.txt']=b'changed by signing tool';fixture.write(path,values,signed=True)
        elif self.mode!='no-signature':
            with zipfile.ZipFile(path,'a') as z:z.writestr('AppxSignature.p7x',b'PKCXsynthetic Windows provider')
        Path(log).write_text('synthetic signature provider; no native certificate was used')
    def verify(self,path,log):
        self.calls.append('verify')
        value=self.identity();value.pop('hasPrivateKey');value.update(status='Valid',timestamped=True,timestampChainValid=True)
        if self.mode=='certificate':value['certificateSHA256']='f'*64
        if self.mode=='publisher':value['publisher']='CN=Other'
        if self.mode=='timestamp':value['timestamped']=False
        if self.mode=='untrusted-timestamp':value['timestampChainValid']=False
        if self.mode=='untrusted':value['chainValid']=False
        if self.mode=='status':value['status']='HashMismatch'
        return value


class WindowsSignerTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name);self.root.chmod(0o700)
        self.certificate=self.root/'certificate.der';self.certificate.write_bytes(b'synthetic public DER fixture');self.certificate.chmod(0o600)
        self.helper=self.root/'native.ps1';self.helper.write_text('synthetic helper');self.helper.chmod(0o600)
        self.profile={'projectId':'project_test','goalId':'goal_test','target':'windows','expectedSigner':hashlib.sha256(self.certificate.read_bytes()).hexdigest(),
                      'expectedWindowsPublisher':'CN=Company','expectedWindowsPackage':'com.company.feedback','expectedWindowsVersion':'1.0.0.0',
                      'certificateFile':str(self.certificate),'storeLocation':'CurrentUser','signtoolPath':str(Path(sys.executable).resolve()),'signtoolSHA256':hashlib.sha256(Path(sys.executable).read_bytes()).hexdigest(),
                      'helperPath':str(self.helper),'helperSHA256':hashlib.sha256(self.helper.read_bytes()).hexdigest(),'powershellPath':sys.executable,'timestampURL':'https://timestamp.example.test'}
        self.state=self.root/'state';self.state.mkdir(mode=0o700)
        self.config={'stateDir':str(self.state),'profiles':[self.profile]}
        self.config_file=self.root/'config.json';self.config_file.write_text(json.dumps(self.config));self.config_file.chmod(0o600)
        self.input=fixture.write(self.root/'unsigned.msix')
        self.request={'protocolVersion':'windows-1','action':'sign','operationKey':'sign-task_test','projectId':'project_test','goalId':'goal_test','taskId':'task_test','planId':'plan_test','runId':'run_test',
                      'requirementRevision':1,'signingInputDigest':'b'*64,'sourceBindingDigest':'c'*64,'repositoryId':'repository_test','commit':'d'*40,'target':'windows','artifactId':'artifact_test',
                      'artifactDigest':hashlib.sha256(self.input.read_bytes()).hexdigest(),'artifactSize':self.input.stat().st_size,'artifactPath':str(self.input),
                      **{k:self.profile[k] for k in ['expectedSigner','expectedWindowsPublisher','expectedWindowsPackage','expectedWindowsVersion']}}

    def engine(self,mode='ok'):
        tools=WindowsProvider(self.profile,mode);return windows_signer.Signer(windows_signer.load_config(self.config_file),tools=tools),tools

    def test_exact_package_sign_once_and_readonly_replay(self):
        engine,tools=self.engine();raw=self.input.read_bytes();receipt=engine.handle(self.request)
        self.assertEqual(receipt['publisher'],'CN=Company');self.assertEqual(receipt['version'],'1.0.0.0');self.assertEqual(receipt['packageName'],'com.company.feedback')
        self.assertEqual(receipt['certificateSHA256'],self.profile['expectedSigner']);self.assertNotEqual(receipt['artifactDigest'],self.request['artifactDigest'])
        self.assertEqual(receipt['artifactDigest'],hashlib.sha256(Path(receipt['artifactPath']).read_bytes()).hexdigest());self.assertEqual(self.input.read_bytes(),raw)
        replay=engine.handle({**self.request,'runId':'run_retry','artifactPath':str(self.root/'missing-download')})
        self.assertEqual(receipt,replay);self.assertEqual(tools.calls.count('sign'),1)
        with sqlite3.connect(self.state/'windows-operations.sqlite') as db:
            row=db.execute('SELECT status,request,result FROM operations').fetchone();self.assertEqual(row[0],'signed')
            self.assertNotIn('artifactPath',json.loads(row[1]));self.assertEqual(json.loads(row[2]),receipt)
        info=engine.inspect('sign-task_test');self.assertEqual(info['status'],'signed');self.assertNotIn('artifactPath',info)
        Path(receipt['artifactPath']).write_bytes(b'tampered')
        with self.assertRaises(ValueError):engine.handle(self.request)
        self.assertEqual(tools.calls.count('sign'),1)

    def test_incomplete_signing_intent_is_never_reexecuted(self):
        engine,tools=self.engine('fail')
        with self.assertRaises(ValueError):engine.handle(self.request)
        tools.mode='ok'
        with self.assertRaisesRegex(ValueError,'reconciliation'):engine.handle(self.request)
        self.assertEqual(tools.calls.count('sign'),1);self.assertEqual(engine.inspect('sign-task_test')['status'],'signing_intent')

    def test_signature_identity_timestamp_and_payload_must_all_pass(self):
        for mode in ['content','no-signature','certificate','publisher','timestamp','untrusted-timestamp','untrusted','status']:
            with self.subTest(mode=mode):
                self.config['stateDir']=str(self.root/('state-'+mode));Path(self.config['stateDir']).mkdir(mode=0o700);self.config_file.write_text(json.dumps(self.config))
                engine,tools=self.engine(mode)
                with self.assertRaises(ValueError):engine.handle(self.request)
                self.assertEqual(engine.inspect('sign-task_test')['status'],'signing_intent')
                with self.assertRaises(ValueError):engine.handle(self.request)
                self.assertEqual(tools.calls.count('sign'),1)

    def test_changed_identity_source_or_config_cannot_reuse_operation(self):
        engine,tools=self.engine();engine.handle(self.request)
        for key,value in [('artifactDigest','f'*64),('commit','f'*40),('expectedWindowsPublisher','CN=Other'),('expectedWindowsVersion','2.0.0.0'),('signingInputDigest','f'*64),('requirementRevision',2)]:
            with self.subTest(key=key):
                with self.assertRaises(ValueError):engine.handle({**self.request,key:value})
        engine.config['profiles'][0]['timestampURL']='https://changed.example.test'
        with self.assertRaises(ValueError):engine.handle(self.request)
        self.assertEqual(tools.calls.count('sign'),1)

    def test_concurrent_calls_share_one_durable_signature(self):
        engine,tools=self.engine()
        with ThreadPoolExecutor(max_workers=2) as pool:results=list(pool.map(lambda _:engine.handle(dict(self.request)),range(2)))
        self.assertEqual(results[0],results[1]);self.assertEqual(tools.calls.count('sign'),1)

    def test_unsafe_config_request_privacy_and_byte_drift_stop_signing(self):
        engine,tools=self.engine()
        for change in [{'artifactSize':True},{'requirementRevision':1.5},{'command':['evil']},{'action':'resign'},{'target':'macos'},{'operationKey':'sign-task_other'},{'expectedWindowsVersion':'01.0.0.0'},{'artifactPath':'relative.msix'}]:
            with self.subTest(change=change):
                with self.assertRaises(ValueError):engine.handle({**self.request,**change})
        with self.assertRaises(ValueError):windows_signer.decode_request(json.dumps(self.request).replace('"action": "sign"','"action":"sign","Action":"sign"').encode())
        self.input.write_bytes(b'changed')
        with self.assertRaises(ValueError):engine.handle(self.request)
        self.assertNotIn('sign',tools.calls)
        self.config_file.chmod(0o644)
        with self.assertRaises(ValueError):windows_signer.load_config(self.config_file)
        self.config_file.chmod(0o600)
        for key,value in [('timestampURL','http://timestamp.example.test'),('storeLocation','Other'),('helperSHA256','f'*64),('signtoolSHA256','f'*64)]:
            candidate=json.loads(json.dumps(self.config));candidate['profiles'][0][key]=value;self.config_file.write_text(json.dumps(candidate))
            with self.assertRaises(ValueError):windows_signer.load_config(self.config_file)

    def test_non_windows_cli_rejects_without_state_or_secret_output(self):
        before=set(self.state.iterdir())
        p=subprocess.run([sys.executable,str(Path(__file__).with_name('sign-windows-msix.py')),'--config',str(self.config_file)],input=json.dumps(self.request),capture_output=True,text=True)
        self.assertNotEqual(p.returncode,0);self.assertNotIn(str(self.config_file),p.stdout+p.stderr);self.assertEqual(set(self.state.iterdir()),before)

    def test_fixed_native_subprocess_contract_and_real_private_journal(self):
        native=self.root/'signtool'
        native.write_text('#!'+sys.executable+'\n'+'''import json,os,sys,zipfile
from pathlib import Path
assert not any(k.startswith('IS_') for k in os.environ)
args=sys.argv[1:]
if args[0]=='sign':
 assert args[1:3]==['/fd','SHA256'] and '/a' not in args and '/f' not in args and '/p' not in args
 assert args[args.index('/sha1')+1]=='''+repr(hashlib.sha1(self.certificate.read_bytes()).hexdigest().upper())+'''
 assert args[args.index('/tr')+1]=='https://timestamp.example.test' and args[args.index('/td')+1]=='SHA256'
 with zipfile.ZipFile(args[-1],'a') as z:z.writestr('AppxSignature.p7x',b'PKCXsynthetic native executable provider')
else:assert args[:-1]==['verify','/pa','/all','/v','/tw']
print('synthetic Windows SDK executable; native trust not established')
''');native.chmod(0o700)
        power=self.root/'powershell'
        identity=WindowsProvider(self.profile).identity()
        power.write_text('#!'+sys.executable+'\n'+'''import json,os,sys
assert not any(k.startswith('IS_') for k in os.environ)
assert sys.argv[1:]==['-NoProfile','-NonInteractive','-File','''+repr(str(self.helper))+''']
r=json.load(sys.stdin)
value='''+repr(identity)+'''
if r['action']=='privacy':value={'private':True}
elif r['action']=='verify':value.pop('hasPrivateKey');value.update(status='Valid',timestamped=True,timestampChainValid=True)
else:assert r['action']=='identity' and r['certificateSHA1']==value['certificateSHA1'] and r['storeLocation']=='CurrentUser'
print(json.dumps(value))
''');power.chmod(0o700)
        self.profile.update(signtoolPath=str(native),signtoolSHA256=hashlib.sha256(native.read_bytes()).hexdigest(),powershellPath=str(power))
        self.config_file.write_text(json.dumps(self.config))
        engine=windows_signer.Signer(windows_signer.load_config(self.config_file))
        result=engine.handle(self.request)
        self.assertTrue(Path(result['artifactPath']).is_file());self.assertNotEqual(result['artifactDigest'],self.request['artifactDigest'])
        with zipfile.ZipFile(result['artifactPath']) as z:self.assertEqual(z.read('app/data/flutter_assets/content.txt'),b'approved Windows resource')
        before=sorted(p.name for p in self.state.iterdir())
        self.assertEqual(engine.handle(self.request),result)
        self.assertEqual(engine.inspect('sign-task_test')['status'],'signed')
        # Replay may recheck ACLs but cannot run the signing executable again.
        with sqlite3.connect(self.state/'windows-operations.sqlite') as db:self.assertEqual(db.execute('SELECT count(*) FROM operations').fetchone()[0],1)


def export_cli_fixture(root,project,goal,mode='ok'):
    """Exercise the real private Python service with only Windows trust substituted."""
    root=Path(root);root.mkdir(mode=0o700,parents=True);state=root/'state';state.mkdir(mode=0o700)
    certificate=root/'certificate.der';certificate.write_bytes(b'synthetic public DER fixture');certificate.chmod(0o600)
    helper=root/'helper.ps1';helper.write_text('synthetic helper');helper.chmod(0o600)
    native=str(Path(sys.executable).resolve())
    profile={'projectId':project,'goalId':goal,'target':'windows','expectedSigner':hashlib.sha256(certificate.read_bytes()).hexdigest(),
        'expectedWindowsPublisher':'CN=Company','expectedWindowsPackage':'com.company.feedback','expectedWindowsVersion':'1.0.0.0','certificateFile':str(certificate),
        'storeLocation':'CurrentUser','signtoolPath':native,'signtoolSHA256':hashlib.sha256(Path(native).read_bytes()).hexdigest(),
        'powershellPath':native,'helperPath':str(helper),'helperSHA256':hashlib.sha256(helper.read_bytes()).hexdigest(),'timestampURL':'https://timestamp.example.test'}
    config=root/'config.json';config.write_text(json.dumps({'stateDir':str(state),'profiles':[profile]}));config.chmod(0o600)
    source=fixture.write(root/'source.msix')
    program=root/'provider.py'
    program.write_text('import json,os,sys\nfrom pathlib import Path\nsys.path.insert(0,'+repr(str(Path(__file__).parent))+')\n'
        'from windows_signer import Signer,load_config,decode_request\nfrom test_windows_signer import WindowsProvider\n'
        'assert not any(k.startswith("IS_") for k in os.environ)\n'
        'config=load_config('+repr(str(config))+')\n'
        'result=Signer(config,tools=WindowsProvider(config["profiles"][0],'+repr(mode)+')).handle(decode_request(sys.stdin.buffer.read(65537)))\n'
        'print(json.dumps(result))\n');program.chmod(0o600)
    return {'python':native,'program':str(program),'config':str(config),'source':base64.b64encode(source.read_bytes()).decode(),'expectedSigner':profile['expectedSigner']}


if __name__=='__main__':unittest.main()
