"""Private Windows certificate-store signing with exact MSIX and durable intent."""
from contextlib import contextmanager, closing
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import sqlite3
import stat
import subprocess
import time
from urllib.parse import urlsplit

import windows_msix as msix

HEX=re.compile(r'[a-f0-9]{64}')
PROFILE={'projectId','goalId','target','expectedSigner','expectedWindowsPublisher','expectedWindowsPackage','expectedWindowsVersion','certificateFile','storeLocation','signtoolPath','signtoolSHA256','powershellPath','helperPath','helperSHA256','timestampURL'}
REQUEST={'protocolVersion','action','operationKey','projectId','goalId','taskId','planId','runId','requirementRevision','signingInputDigest','sourceBindingDigest','repositoryId','commit','target','artifactId','artifactDigest','artifactSize','artifactPath','expectedSigner','expectedWindowsPublisher','expectedWindowsPackage','expectedWindowsVersion'}


def canonical(value):return json.dumps(value,sort_keys=True,separators=(',',':'),ensure_ascii=True)


def strict_json(body):
    def pairs(items):
        out={};seen=set()
        for key,value in items:
            if key.casefold() in seen:raise ValueError('ambiguous signing JSON')
            seen.add(key.casefold());out[key]=value
        return out
    try:return json.loads(body,object_pairs_hook=pairs,parse_constant=lambda _:(_ for _ in ()).throw(ValueError('invalid JSON number')))
    except (UnicodeError,json.JSONDecodeError,RecursionError) as error:raise ValueError('invalid signing JSON') from error


def fields(value,expected):
    if not isinstance(value,dict) or set(value)!=expected:raise ValueError('exact signing fields required')


def no_link(path):
    info=path.lstat()
    if stat.S_ISLNK(info.st_mode) or getattr(info,'st_file_attributes',0)&0x400:raise ValueError('signing paths cannot be links or reparse points')
    return info


def private_directory(path):
    info=no_link(path)
    if not path.is_absolute() or not stat.S_ISDIR(info.st_mode) or (os.name!='nt' and (info.st_mode&0o077 or info.st_uid!=os.getuid())):raise ValueError('private owned signing directory required')


def private_bytes(path,limit):
    if not path.is_absolute():raise ValueError('absolute private file required')
    before=no_link(path)
    if not stat.S_ISREG(before.st_mode) or before.st_size>limit or (os.name!='nt' and (before.st_mode&0o077 or before.st_uid!=os.getuid())):raise ValueError('bounded private regular file required')
    with path.open('rb') as stream:
        if not os.path.samestat(before,os.fstat(stream.fileno())):raise ValueError('private file changed')
        body=stream.read(limit+1)
    if len(body)>limit:raise ValueError('private file limit exceeded')
    return body


def regular_hash(path):
    before=no_link(path)
    if not stat.S_ISREG(before.st_mode) or not 0<before.st_size<=msix.MAX_ARCHIVE:raise ValueError('bounded regular signing file required')
    h=hashlib.sha256();size=0
    with path.open('rb') as stream:
        if not os.path.samestat(before,os.fstat(stream.fileno())):raise ValueError('signing file changed')
        while chunk:=stream.read(1<<20):
            h.update(chunk);size+=len(chunk)
            if size>msix.MAX_ARCHIVE:raise ValueError('signing file limit exceeded')
    return h.hexdigest(),size


def sync_directory(path):
    if os.name=='nt':return  # File FlushFileBuffers is provided by fsync; directory fsync is unavailable on Windows.
    fd=os.open(path,os.O_RDONLY)
    try:os.fsync(fd)
    finally:os.close(fd)


def write_private(path,body):
    with path.open('xb') as stream:
        os.chmod(path,0o600);stream.write(body);stream.flush();os.fsync(stream.fileno())
    sync_directory(path.parent)


def load_config(path):
    path=Path(path).absolute();config=strict_json(private_bytes(path,1<<20));fields(config,{'stateDir','profiles'})
    if not isinstance(config['stateDir'],str) or not Path(config['stateDir']).is_absolute():raise ValueError('absolute pre-provisioned state required')
    private_directory(Path(config['stateDir']))
    if not isinstance(config['profiles'],list) or not 1<=len(config['profiles'])<=100:raise ValueError('fixed signing profiles required')
    seen=set()
    for profile in config['profiles']:
        fields(profile,PROFILE)
        if not all(isinstance(v,str) for v in profile.values()):raise ValueError('fixed signing profile strings required')
        key=(profile['projectId'],profile['goalId'],profile['target'])
        if (key in seen or profile['target']!='windows' or profile['storeLocation'] not in {'CurrentUser','LocalMachine'} or not HEX.fullmatch(profile['expectedSigner'])
                or not msix.valid_publisher(profile['expectedWindowsPublisher']) or not msix.PACKAGE.fullmatch(profile['expectedWindowsPackage']) or not msix.valid_version(profile['expectedWindowsVersion'])):raise ValueError('invalid Windows signing profile')
        seen.add(key)
        for name,prefix in [('projectId','project_'),('goalId','goal_')]:
            if not re.fullmatch(prefix+r'[A-Za-z0-9_-]{1,100}',profile[name]):raise ValueError('invalid fixed signing scope')
        url=urlsplit(profile['timestampURL'])
        if url.scheme!='https' or not url.hostname or url.username or url.password or url.query or url.fragment or url.port==0:raise ValueError('fixed HTTPS timestamp service required')
        for name,pin,limit in [('certificateFile','expectedSigner',1<<20),('helperPath','helperSHA256',1<<20),('signtoolPath','signtoolSHA256',128<<20)]:
            file=Path(profile[name])
            # SDK executables may be publicly readable; credentials/helper/config may not be writable by other identities.
            body=private_bytes(file,limit) if name!='signtoolPath' else file.read_bytes()
            if not file.is_absolute() or file.is_symlink() or not file.is_file() or len(body)>limit or not HEX.fullmatch(profile[pin]) or hashlib.sha256(body).hexdigest()!=profile[pin]:raise ValueError('pinned signing file changed')
            if file.resolve().is_relative_to(Path(config['stateDir']).resolve()):raise ValueError('administrator signing files must remain outside state')
        power=Path(profile['powershellPath'])
        if not power.is_absolute() or not power.is_file() or not os.access(power,os.X_OK):raise ValueError('fixed PowerShell executable required')
    return config


def validate_request(request):
    fields(request,REQUEST)
    if (not all(isinstance(v,str) for k,v in request.items() if k not in {'artifactSize','requirementRevision'})
            or request['protocolVersion']!='windows-1' or request['action']!='sign' or request['target']!='windows'
            or type(request['artifactSize']) is not int or not 0<request['artifactSize']<=msix.MAX_ARCHIVE
            or type(request['requirementRevision']) is not int or request['requirementRevision']<1
            or request['operationKey']!='sign-'+request['taskId'] or not re.fullmatch(r'[a-f0-9]{40}',request['commit'])
            or not Path(request['artifactPath']).is_absolute() or not msix.valid_publisher(request['expectedWindowsPublisher'])
            or not msix.PACKAGE.fullmatch(request['expectedWindowsPackage']) or not msix.valid_version(request['expectedWindowsVersion'])
            or any(not HEX.fullmatch(request[k]) for k in ['artifactDigest','expectedSigner','signingInputDigest','sourceBindingDigest'])):raise ValueError('invalid authoritative Windows request')
    for key,prefix in [('projectId','project_'),('goalId','goal_'),('taskId','task_'),('planId','plan_'),('runId','run_'),('repositoryId','repository_'),('artifactId','artifact_')]:
        if not re.fullmatch(prefix+r'[A-Za-z0-9_-]{1,100}',request[key]):raise ValueError('invalid signing identity')


def decode_request(body):
    if len(body)>65536:raise ValueError('signing request exceeds limit')
    request=strict_json(body);validate_request(request);return request


@contextmanager
def maintenance(root):
    path=root/'maintenance.lock'
    if path.exists() or path.is_symlink():no_link(path)
    with path.open('a+b') as stream:
        os.chmod(path,0o600)
        if os.name=='nt':
            import msvcrt
            stream.seek(0);stream.write(b'0');stream.flush();stream.seek(0);msvcrt.locking(stream.fileno(),msvcrt.LK_LOCK,1)
        else:
            import fcntl
            fcntl.flock(stream.fileno(),fcntl.LOCK_EX)
        try:yield
        finally:
            if os.name=='nt':stream.seek(0);msvcrt.locking(stream.fileno(),msvcrt.LK_UNLCK,1)
            else:fcntl.flock(stream.fileno(),fcntl.LOCK_UN)


class NativeTools:
    def __init__(self,profile,root):self.profile,self.root=profile,root

    def run(self,argv,body,log):
        environment={k:os.environ[k] for k in ['SystemRoot','USERPROFILE','APPDATA','LOCALAPPDATA','PROGRAMDATA'] if k in os.environ}
        environment.update(TEMP=str(self.root),TMP=str(self.root),PATH=str(Path(self.profile['signtoolPath']).parent))
        with Path(log).open('xb') as stream:
            os.chmod(log,0o600)
            proc=subprocess.Popen(argv,stdin=subprocess.PIPE,stdout=stream,stderr=stream,env=environment,cwd=self.root,
                **({'creationflags':subprocess.CREATE_NEW_PROCESS_GROUP} if os.name=='nt' else {'start_new_session':True}))
            try:proc.communicate(body,timeout=180)
            finally:
                if os.name=='nt':
                    subprocess.run([str(Path(os.environ['SystemRoot'])/'System32/taskkill.exe'),'/T','/F','/PID',str(proc.pid)],capture_output=True,timeout=10)
                else:
                    try:os.killpg(proc.pid,signal.SIGKILL)
                    except ProcessLookupError:pass
                if proc.poll() is None:proc.kill()
                proc.wait(timeout=10)
            stream.flush();os.fsync(stream.fileno())
        output=private_bytes(Path(log),8<<20)
        if proc.returncode:raise ValueError('native signing did not confirm success')
        return output

    def helper(self,request,name):
        path=self.root/(name+'-'+str(time.time_ns())+'.log')
        output=self.run([self.profile['powershellPath'],'-NoProfile','-NonInteractive','-File',self.profile['helperPath']],canonical(request).encode(),path)
        return strict_json(output.decode('utf-8-sig'))

    def privacy(self,paths):
        if self.helper({'action':'privacy','paths':paths},'privacy')!={'private':True}:raise ValueError('private Windows DACL required')

    def identity(self):
        body=private_bytes(Path(self.profile['certificateFile']),1<<20)
        return self.helper({'action':'identity','storeLocation':self.profile['storeLocation'],'certificateSHA1':hashlib.sha1(body).hexdigest().upper()},'identity')

    def sign(self,path,log):
        body=private_bytes(Path(self.profile['certificateFile']),1<<20)
        argv=[self.profile['signtoolPath'],'sign','/fd','SHA256','/sha1',hashlib.sha1(body).hexdigest().upper(),'/s','My']
        if self.profile['storeLocation']=='LocalMachine':argv.append('/sm')
        argv+=['/tr',self.profile['timestampURL'],'/td','SHA256',str(path)]
        self.run(argv,None,log)

    def verify(self,path,log):
        self.run([self.profile['signtoolPath'],'verify','/pa','/all','/v','/tw',str(path)],None,log)
        return self.helper({'action':'verify','path':str(path)},'verify')


class Signer:
    def __init__(self,config,tools=None):self.config,self.injected=config,tools

    def database(self,readonly=False):
        path=Path(self.config['stateDir'])/'windows-operations.sqlite'
        if path.exists() or path.is_symlink():private_bytes(path,128<<20)
        elif readonly:raise ValueError('unknown signing operation')
        else:write_private(path,b'')
        if readonly:return sqlite3.connect(path.as_uri()+'?mode=ro',uri=True,timeout=5)
        db=sqlite3.connect(path,timeout=5);db.execute('PRAGMA journal_mode=DELETE');db.execute('PRAGMA synchronous=FULL')
        db.execute('CREATE TABLE IF NOT EXISTS operations (key TEXT PRIMARY KEY,binding TEXT NOT NULL,status TEXT NOT NULL,request TEXT NOT NULL,result TEXT NOT NULL,updated TEXT NOT NULL)');db.commit();return db

    def save(self,db,request,binding,status,result):
        db.execute('INSERT INTO operations VALUES(?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET status=excluded.status,result=excluded.result,updated=excluded.updated',
                   (request['operationKey'],binding,status,canonical(request),canonical(result),str(time.time())))
        db.commit();sync_directory(Path(self.config['stateDir']))

    def inspect(self,key):
        if not re.fullmatch(r'sign-task_[A-Za-z0-9_-]{1,100}',key):raise ValueError('invalid signing operation')
        with maintenance(Path(self.config['stateDir'])),closing(self.database(readonly=True)) as db:
            row=db.execute('SELECT binding,status,result FROM operations WHERE key=?',(key,)).fetchone()
            if row is None:raise ValueError('unknown signing operation')
            return {'operationKey':key,'requestBinding':row[0],'status':row[1],'artifactDigest':strict_json(row[2]).get('artifactDigest','')}

    def handle(self,request):
        validate_request(request)
        profiles=[p for p in self.config['profiles'] if all(p[k]==request[k] for k in ['projectId','goalId','target','expectedSigner','expectedWindowsPublisher','expectedWindowsPackage','expectedWindowsVersion'])]
        if len(profiles)!=1:raise ValueError('exact administrator Windows profile required')
        profile=profiles[0];root=Path(self.config['stateDir']);private_directory(root)
        tools=self.injected or NativeTools(profile,root)
        tools.privacy([{'path':str(root),'protected':True},{'path':str(root.parent),'protected':True},
            {'path':str(Path(profile['certificateFile']).parent),'protected':True},{'path':profile['certificateFile'],'protected':False},
            {'path':str(Path(profile['helperPath']).parent),'protected':True},{'path':profile['helperPath'],'protected':False}])
        stable={k:v for k,v in request.items() if k not in {'runId','artifactPath'}};binding=msix.digest({'request':stable,'profile':profile})
        with maintenance(root),closing(self.database()) as db:
            row=db.execute('SELECT binding,status,result FROM operations WHERE key=?',(request['operationKey'],)).fetchone()
            if row:
                if row[0]!=binding:raise ValueError('signing request or configuration changed')
                if row[1]!='signed':raise ValueError('incomplete signing intent requires administrator reconciliation')
                receipt=strict_json(row[2]);observed,size=regular_hash(Path(receipt['artifactPath']))
                if observed!=receipt['artifactDigest'] or size!=receipt['artifactSize']:raise ValueError('completed MSIX changed')
                return receipt
            tools.privacy([{'path':request['artifactPath'],'protected':False},{'path':str(Path(request['artifactPath']).parent),'protected':False}])
            body=private_bytes(Path(request['artifactPath']),msix.MAX_ARCHIVE)
            if len(body)!=request['artifactSize'] or hashlib.sha256(body).hexdigest()!=request['artifactDigest']:raise ValueError('unsigned input differs')
            opdir=root/('op-'+hashlib.sha256(request['operationKey'].encode()).hexdigest());opdir.mkdir(mode=0o700);sync_directory(root)
            source=opdir/'unsigned.msix';write_private(source,body);metadata=msix.inspect(source)
            for actual,expected in [('publisher','expectedWindowsPublisher'),('packageName','expectedWindowsPackage'),('version','expectedWindowsVersion')]:
                if metadata[actual]!=profile[expected]:raise ValueError('MSIX differs from approved package identity')
            output=opdir/'signed.msix';write_private(output,body);del body
            self.save(db,stable,binding,'signing_intent',{'payloadDigest':metadata['payloadDigest']})
            identity=tools.identity();self.validate_identity(profile,identity,private=True)
            tools.sign(output,opdir/'sign.log')
            signed=msix.inspect(output,signed=True);msix.same_payload(metadata,signed)
            verified=tools.verify(output,opdir/'verify.log');self.validate_identity(profile,verified,private=False)
            if verified.get('status')!='Valid' or verified.get('timestamped') is not True or verified.get('timestampChainValid') is not True:raise ValueError('trusted Authenticode and timestamp required')
            observed,size=regular_hash(output)
            if observed==request['artifactDigest']:raise ValueError('signature did not change package')
            with output.open('r+b') as stream:stream.flush();os.fsync(stream.fileno())
            sync_directory(opdir)
            receipt={'protocolVersion':'windows-1','state':'complete','operationKey':request['operationKey'],'requestBinding':binding,'payloadDigest':metadata['payloadDigest'],
                     'certificateSHA256':profile['expectedSigner'],'certificateSHA1':identity['certificateSHA1'],'inputArtifactId':request['artifactId'],'inputArtifactDigest':request['artifactDigest'],
                     'artifactPath':str(output),'artifactDigest':observed,'artifactSize':size,**{k:metadata[k] for k in ['publisher','packageName','version','architecture']}}
            self.save(db,stable,binding,'signed',receipt);return receipt

    def validate_identity(self,profile,identity,private):
        body=private_bytes(Path(profile['certificateFile']),1<<20)
        if (identity.get('certificateSHA256')!=profile['expectedSigner'] or identity.get('certificateSHA1')!=hashlib.sha1(body).hexdigest().upper()
                or identity.get('publisher')!=profile['expectedWindowsPublisher'] or any(identity.get(k) is not True for k in ['codeSigning','chainValid','validTime'])
                or private and identity.get('hasPrivateKey') is not True):raise ValueError('exact trusted code signing certificate required')
