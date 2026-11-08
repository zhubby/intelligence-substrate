"""Administrator-owned, once-only App Store IPA signer; no store/network actions."""
from contextlib import closing
from datetime import datetime, timezone
import base64
import hashlib
import os
from pathlib import Path, PurePosixPath
import plistlib
import re
import sqlite3
import stat
import time
import zipfile

from android_signer_state import canonical, strict_json, private_bytes, private_directory, maintenance
from macos_signer import AppleTools, HEX, TEAM, UUID, MAX_ARCHIVE, fields, digest, regular_hash, write_private, sync_directory
import ios_payload

TOOLS = {'openssl','codesign','security'}
PROFILE = {'projectId','goalId','target','bundleId','expectedSigner','expectedTeamId','expectedProvisioningProfile',
           'certificateFile','profileFile','entitlementsFile','keychainFile','profileTrustCAFile','profileTrustCASHA256','tools'}
REQUEST = {'protocolVersion','action','operationKey','projectId','goalId','taskId','planId','requirementRevision',
           'signingInputDigest','sourceBindingDigest','runId','repositoryId','commit','target','artifactId',
           'artifactDigest','artifactSize','artifactPath','expectedSigner','expectedTeamId','expectedProvisioningProfile'}


def entitlements_file(path):
    body = private_bytes(Path(path),65536)
    try:
        body.decode('ascii'); value = plistlib.loads(body)
    except (UnicodeError,ValueError,plistlib.InvalidFileException) as error:
        raise ValueError('administrator XML ASCII entitlements required') from error
    if not body.startswith(b'<?xml') or not isinstance(value,dict) or not 1 <= len(value) <= 64:
        raise ValueError('bounded explicit entitlements required')
    return value


def load_config(path):
    config = strict_json(private_bytes(Path(path).absolute(),1<<20))
    fields(config,{'stateDir','profiles'})
    root = Path(config['stateDir'])
    if not root.is_absolute(): raise ValueError('absolute private state directory required')
    if root.exists() or root.is_symlink(): private_directory(root)
    if not isinstance(config['profiles'],list) or not 1 <= len(config['profiles']) <= 100: raise ValueError('fixed App Store profiles required')
    seen = set()
    for profile in config['profiles']:
        fields(profile,PROFILE)
        if not all(isinstance(v,str) for k,v in profile.items() if k!='tools'): raise ValueError('fixed profile strings required')
        key = (profile['projectId'],profile['goalId'],profile['target'])
        if (key in seen or profile['target']!='ios-appstore' or not TEAM.fullmatch(profile['expectedTeamId'])
                or any(not HEX.fullmatch(profile[k]) for k in ['expectedSigner','expectedProvisioningProfile','profileTrustCASHA256'])
                or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9.-]{1,200}\.[A-Za-z0-9-]+',profile['bundleId'])):
            raise ValueError('invalid or duplicate App Store profile')
        for name,prefix in [('projectId','project_'),('goalId','goal_')]:
            if not re.fullmatch(prefix+r'[A-Za-z0-9_-]{1,100}',profile[name]): raise ValueError('invalid fixed signing scope')
        seen.add(key)
        for name,limit,expected in [('certificateFile',1<<20,'expectedSigner'),('profileFile',2<<20,'expectedProvisioningProfile'),
                                    ('profileTrustCAFile',1<<20,'profileTrustCASHA256'),('keychainFile',64<<20,None),('entitlementsFile',65536,None)]:
            body = private_bytes(Path(profile[name]),limit)
            if Path(profile[name]).resolve().is_relative_to(root.resolve()): raise ValueError('signing credentials must remain outside state')
            if expected and hashlib.sha256(body).hexdigest()!=profile[expected]: raise ValueError('pinned signing credential changed')
        entitlements_file(profile['entitlementsFile'])
        fields(profile['tools'],TOOLS)
        for name in TOOLS:
            tool = Path(profile['tools'][name])
            if not tool.is_absolute() or not tool.is_file() or not os.access(tool,os.X_OK): raise ValueError('fixed executable Apple/OpenSSL3 tools required')
    return config


def validate_request(request):
    fields(request,REQUEST)
    if (not all(isinstance(v,str) for k,v in request.items() if k not in {'artifactSize','requirementRevision'})
            or request['protocolVersion']!='ios-1' or request['action']!='sign' or request['target']!='ios-appstore'
            or type(request['requirementRevision']) is not int or request['requirementRevision']<1
            or type(request['artifactSize']) is not int or not 0<request['artifactSize']<=MAX_ARCHIVE
            or request['operationKey']!='sign-'+request['taskId'] or not re.fullmatch(r'[a-f0-9]{40}',request['commit'])
            or not TEAM.fullmatch(request['expectedTeamId']) or not Path(request['artifactPath']).is_absolute()
            or any(not HEX.fullmatch(request[k]) for k in ['artifactDigest','expectedSigner','expectedProvisioningProfile','signingInputDigest','sourceBindingDigest'])):
        raise ValueError('invalid authoritative iOS signing request')
    for name,prefix in [('projectId','project_'),('goalId','goal_'),('taskId','task_'),('planId','plan_'),('runId','run_'),('repositoryId','repository_'),('artifactId','artifact_')]:
        if not re.fullmatch(prefix+r'[A-Za-z0-9_-]{1,100}',request[name]): raise ValueError('invalid signing request identity')


def decode_request(body):
    if len(body)>65536: raise ValueError('signing request exceeds limit')
    request = strict_json(body); validate_request(request); return request


def permitted(value, allowance, depth=0):
    if depth>8: return False
    if isinstance(value,str) and isinstance(allowance,str):
        if len(value)>1024 or any(c in value for c in '*?[]'): return False
        if allowance.endswith('*') and allowance.count('*')==1 and not any(c in allowance for c in '?[]'):
            return value.startswith(allowance[:-1])
        return value==allowance
    if type(value) is bool: return type(allowance) is bool and value is allowance
    if isinstance(value,list) and isinstance(allowance,list):
        return len(value)<=64 and len({canonical(v) for v in value})==len(value) and all(any(permitted(v,a,depth+1) for a in allowance) for v in value)
    if isinstance(value,dict) and isinstance(allowance,dict):
        return len(value)<=64 and all(isinstance(k,str) and k in allowance and permitted(v,allowance[k],depth+1) for k,v in value.items())
    return type(value) is int and type(allowance) is int and value==allowance


def validate_profile(provision, profile, entitlements):
    if not isinstance(provision,dict) or not isinstance(entitlements,dict): raise ValueError('profile and entitlements dictionaries required')
    now = datetime.now(timezone.utc)
    def date(name):
        value=provision.get(name)
        if not isinstance(value,datetime): raise ValueError('profile validity dates required')
        return value.replace(tzinfo=timezone.utc) if value.tzinfo is None else value.astimezone(timezone.utc)
    if (date('ExpirationDate')<=now or date('CreationDate')>now or date('CreationDate')>=date('ExpirationDate')
            or provision.get('TeamIdentifier')!=[profile['expectedTeamId']]
            or provision.get('ApplicationIdentifierPrefix')!=[profile['expectedTeamId']]
            or provision.get('Platform')!=['iOS'] or not isinstance(provision.get('UUID'),str)
            or not UUID.fullmatch(provision['UUID'].lower()) or 'ProvisionedDevices' in provision or 'ProvisionsAllDevices' in provision):
        raise ValueError('unexpired explicit App Store distribution profile required')
    certificates = provision.get('DeveloperCertificates')
    if (not isinstance(certificates,list) or not 1<=len(certificates)<=100 or any(not isinstance(c,bytes) or len(c)>1<<20 for c in certificates)
            or sum(hashlib.sha256(c).hexdigest()==profile['expectedSigner'] for c in certificates)!=1):
        raise ValueError('profile does not authorize the exact distribution certificate')
    allowance = provision.get('Entitlements')
    app_id=profile['expectedTeamId']+'.'+profile['bundleId']
    if (not isinstance(allowance,dict) or allowance.get('get-task-allow') is not False or entitlements.get('get-task-allow') is not False
            or any(m.get('application-identifier')!=app_id or m.get('com.apple.developer.team-identifier')!=profile['expectedTeamId'] for m in [allowance,entitlements])
            or not permitted(entitlements,allowance)):
        raise ValueError('approved non-debug entitlements must fit the exact profile')


class Signer:
    def __init__(self,config,tools=None): self.config,self.injected_tools=config,tools

    def database(self):
        root=Path(self.config['stateDir']); root.mkdir(mode=0o700,parents=True,exist_ok=True); private_directory(root)
        path=root/'ios-operations.sqlite'
        if path.exists() or path.is_symlink(): private_bytes(path,128<<20)
        else: write_private(path,b'')
        db=sqlite3.connect(path,timeout=5)
        db.execute('PRAGMA synchronous=FULL'); db.execute('PRAGMA journal_mode=DELETE')
        db.execute('CREATE TABLE IF NOT EXISTS operations (key TEXT PRIMARY KEY,binding TEXT NOT NULL,status TEXT NOT NULL,request TEXT NOT NULL,result TEXT NOT NULL,updated TEXT NOT NULL)')
        db.commit(); return db

    def save(self,db,request,binding,status,data):
        db.execute('INSERT INTO operations VALUES(?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET status=excluded.status,result=excluded.result,updated=excluded.updated',
            (request['operationKey'],binding,status,canonical(request),canonical(data),str(time.time())))
        db.commit(); sync_directory(Path(self.config['stateDir']))

    def inspect(self,key):
        if not re.fullmatch(r'sign-task_[A-Za-z0-9_-]{1,100}',key): raise ValueError('invalid signing operation')
        with maintenance(self.config,exclusive=True),closing(self.database()) as db:
            row=db.execute('SELECT binding,status,result FROM operations WHERE key=?',(key,)).fetchone()
            if row is None: raise ValueError('unknown signing operation')
            return {'operationKey':key,'requestBinding':row[0],'status':row[1],'artifactDigest':strict_json(row[2]).get('artifactDigest','')}

    def handle(self,request):
        validate_request(request)
        profiles=[p for p in self.config['profiles'] if all(p[k]==request[k] for k in ['projectId','goalId','target','expectedSigner','expectedTeamId','expectedProvisioningProfile'])]
        if len(profiles)!=1: raise ValueError('exact administrator App Store profile required')
        profile=profiles[0]
        entitlements=entitlements_file(profile['entitlementsFile'])
        stable={k:v for k,v in request.items() if k not in {'runId','artifactPath'}}
        binding=digest({'request':stable,'profile':profile,'entitlements':entitlements})
        with maintenance(self.config,exclusive=True),closing(self.database()) as db:
            row=db.execute('SELECT binding,status,result FROM operations WHERE key=?',(request['operationKey'],)).fetchone()
            if row:
                if row[0]!=binding: raise ValueError('signing request or configuration changed')
                if row[1]!='signed': raise ValueError('incomplete signing intent requires administrator reconciliation')
                receipt=strict_json(row[2]); observed,size=regular_hash(receipt['artifactPath'])
                if observed!=receipt['artifactDigest'] or size!=receipt['artifactSize']: raise ValueError('completed IPA changed')
                return receipt
            body=private_bytes(Path(request['artifactPath']),MAX_ARCHIVE)
            if len(body)!=request['artifactSize'] or hashlib.sha256(body).hexdigest()!=request['artifactDigest']: raise ValueError('unsigned input differs from exact source')
            opdir=Path(self.config['stateDir'])/('op-'+hashlib.sha256(request['operationKey'].encode()).hexdigest())
            opdir.mkdir(mode=0o700); sync_directory(opdir.parent)
            snapshot=opdir/'unsigned.tar.gz'; write_private(snapshot,body); del body
            metadata=ios_payload.inspect(snapshot,opdir/'application')
            if metadata['bundleId']!=profile['bundleId']: raise ValueError('app bundle differs from fixed profile')
            self.save(db,stable,binding,'signing_intent',{'payloadDigest':metadata['payloadDigest']})
            tools=self.injected_tools or AppleTools(profile)
            identity=self.credentials(profile,tools,opdir,entitlements)
            self.sign(profile,tools,opdir,metadata,identity,entitlements)
            artifact=opdir/'signed.ipa'; self.package(metadata,artifact)
            output_digest,size=regular_hash(artifact)
            receipt={'protocolVersion':'ios-1','state':'complete','operationKey':request['operationKey'],'requestBinding':binding,
                'payloadDigest':metadata['payloadDigest'],'certificateSHA256':request['expectedSigner'],'profileSHA256':request['expectedProvisioningProfile'],
                'teamId':request['expectedTeamId'],'inputArtifactId':request['artifactId'],'inputArtifactDigest':request['artifactDigest'],
                'artifactPath':str(artifact),'artifactDigest':output_digest,'artifactSize':size,
                'bundleId':metadata['bundleId'],'version':metadata['version'],'build':metadata['build']}
            self.save(db,stable,binding,'signed',receipt)
            return receipt

    def credentials(self,profile,tools,directory,entitlements):
        code,text,_=tools.run('openssl',directory,'version')
        if code or not text.startswith('OpenSSL 3.'): raise ValueError('administrator OpenSSL3 required for explicit CMS trust stores')
        output=directory/'authenticated-profile.plist'
        code,_,_=tools.run('openssl',directory,'cms','-verify','-binary','-inform','DER','-in',profile['profileFile'],
            '-CAfile',profile['profileTrustCAFile'],'-no-CApath','-no-CAstore','-purpose','any','-out',output)
        if code: raise ValueError('provisioning CMS signature or configured trust chain failed')
        output.chmod(0o600)
        try: provision=plistlib.loads(private_bytes(output,2<<20))
        except (ValueError,plistlib.InvalidFileException) as error: raise ValueError('invalid authenticated provisioning profile') from error
        validate_profile(provision,profile,entitlements)
        body=private_bytes(Path(profile['certificateFile']),1<<20)
        if hashlib.sha256(body).hexdigest()!=profile['expectedSigner']: raise ValueError('pinned distribution certificate changed')
        code,text,_=tools.run('openssl',directory,'x509','-inform','DER','-in',profile['certificateFile'],'-noout','-subject','-nameopt','sep_multiline,sname,utf8','-checkend','0')
        subject=dict(re.findall(r'^\s*(CN|OU)\s*=\s*(.+?)\s*$',text,re.M))
        if code or subject.get('OU')!=profile['expectedTeamId'] or not subject.get('CN','').startswith(('Apple Distribution: ','iPhone Distribution: ')):
            raise ValueError('pinned Apple distribution certificate and team required')
        code,text,_=tools.run('security',directory,'find-certificate','-a','-p',profile['keychainFile'])
        certificates=[base64.b64decode(re.sub(r'\s','',c),validate=True) for c in re.findall(r'-----BEGIN CERTIFICATE-----\s*([A-Za-z0-9+/=\s]+?)\s*-----END CERTIFICATE-----',text)]
        if code or sum(hashlib.sha256(c).hexdigest()==profile['expectedSigner'] for c in certificates)!=1:
            raise ValueError('exact distribution certificate must exist once in private keychain')
        return hashlib.sha1(body).hexdigest()

    def sign(self,profile,tools,directory,metadata,identity,entitlements):
        app=Path(metadata['app']); extracted=app.parent
        code,text,error=tools.run('codesign',directory,'--display','--verbose=4',app)
        if code!=1 or 'code object is not signed at all' not in text+error: raise ValueError('top-level iOS application must be confirmed unsigned')
        for name in metadata['code']:
            code,text,error=tools.run('codesign',directory,'--display',extracted/name)
            if code==0:
                code,_,_=tools.run('codesign',directory,'--verify','--strict',extracted/name)
                if code: raise ValueError('invalid embedded framework signature')
            elif code!=1 or 'code object is not signed at all' not in text+error: raise ValueError('unknown embedded signature state')
        provisioning=private_bytes(Path(profile['profileFile']),2<<20)
        if hashlib.sha256(provisioning).hexdigest()!=profile['expectedProvisioningProfile']: raise ValueError('profile changed before embedding')
        (app/'embedded.mobileprovision').write_bytes(provisioning); (app/'embedded.mobileprovision').chmod(0o644)
        requirement='anchor apple generic and certificate leaf[subject.OU] = "'+profile['expectedTeamId']+'" and certificate 1[field.1.2.840.113635.100.6.2.1] exists and certificate leaf[field.1.2.840.113635.100.6.1.4] exists'
        for index,name in enumerate(metadata['signables']):
            path=extracted/name
            args=['--force','--sign',identity,'--keychain',profile['keychainFile'],'--timestamp=none','--generate-entitlement-der']
            if path==app: args+=['--entitlements',profile['entitlementsFile']]
            code,_,_=tools.run('codesign',directory,*args,path)
            if code: raise ValueError('fixed iOS code signing failed')
            prefix=str(directory/('certificate-'+str(index)+'-'))
            code,text,error=tools.run('codesign',directory,'--display','--verbose=4','--extract-certificates',prefix,path)
            certificate,_=regular_hash(prefix+'0',1<<20)
            if code or certificate!=profile['expectedSigner'] or 'TeamIdentifier='+profile['expectedTeamId'] not in text+error:
                raise ValueError('signed native object differs from approved certificate or team')
            code,_,_=tools.run('codesign',directory,'--verify','--strict','-R',requirement,path)
            if code: raise ValueError('Apple distribution signature verification failed')
        code,_,_=tools.run('codesign',directory,'--verify','--deep','--strict','-R',requirement,app)
        if code: raise ValueError('complete iOS application signature failed')
        code,text,_=tools.run('codesign',directory,'--display','--entitlements',':-',app)
        try: observed=plistlib.loads(text.encode())
        except (ValueError,plistlib.InvalidFileException) as error: raise ValueError('signed app entitlements unavailable') from error
        if code or canonical(observed)!=canonical(entitlements): raise ValueError('signed entitlements differ from administrator approval')
        if (app/'embedded.mobileprovision').read_bytes()!=provisioning: raise ValueError('signed app profile changed')
        expected={n['path']:n for n in metadata['nodes'] if '_CodeSignature' not in PurePosixPath(n['path']).parts}
        observed={app.name}
        for current,dirs,files in os.walk(app,followlinks=False):
            dirs[:]=[d for d in dirs if d!='_CodeSignature']
            for name in [*dirs,*files]:
                path=Path(current)/name; relative=path.relative_to(extracted).as_posix()
                if path==app/'embedded.mobileprovision': continue
                observed.add(relative); node=expected.get(relative); mode=path.lstat().st_mode
                if node is None or stat.S_IMODE(mode)!=node['mode']: raise ValueError('signing changed resource path or mode')
                if node['type']=='dir':
                    if not stat.S_ISDIR(mode): raise ValueError('signing changed directory type')
                elif node['type']!='file' or not stat.S_ISREG(mode) or (relative not in metadata['code'] and regular_hash(path,allow_empty=True)[0]!=node['sha256']):
                    raise ValueError('signing changed ordinary application resources')
        if observed!=set(expected): raise ValueError('signing removed application resources')

    def package(self,metadata,output):
        app=Path(metadata['app']); entries=sorted(app.rglob('*')); records={}; total=0
        if len(entries)>50000: raise ValueError('signed IPA entry limit exceeded')
        with output.open('xb') as stream:
            with zipfile.ZipFile(stream,'w',compression=zipfile.ZIP_DEFLATED,compresslevel=6) as package:
                for path in entries:
                    mode=path.lstat().st_mode; name='Payload/'+path.relative_to(app.parent).as_posix()
                    if stat.S_ISDIR(mode): body=b''; name+='/'
                    elif stat.S_ISREG(mode):
                        actual,size=regular_hash(path,allow_empty=True); total+=size
                        if total>768<<20: raise ValueError('signed IPA expansion limit exceeded')
                        body=path.read_bytes()
                        if hashlib.sha256(body).hexdigest()!=actual: raise ValueError('signed file changed before archiving')
                    else: raise ValueError('signed IPA cannot contain links or special files')
                    info=zipfile.ZipInfo(name,date_time=(1980,1,1,0,0,0)); info.create_system=3
                    info.external_attr=mode<<16; info.compress_type=zipfile.ZIP_DEFLATED
                    package.writestr(info,body); records[name]=hashlib.sha256(body).hexdigest()
            stream.flush(); os.fsync(stream.fileno())
        output.chmod(0o600); sync_directory(output.parent)
        regular_hash(output)
        with zipfile.ZipFile(output) as package:
            if len(package.namelist())!=len(records) or set(package.namelist())!=set(records): raise ValueError('IPA paths differ from signed application')
            for name,h in records.items():
                if hashlib.sha256(package.read(name)).hexdigest()!=h: raise ValueError('IPA content differs from signed application')
