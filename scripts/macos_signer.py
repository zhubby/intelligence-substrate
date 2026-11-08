"""Private, administrator-owned macOS signing and notarization state machine.

No repository command, executable, entitlement, credential or keychain selection
is accepted. The only network mutation is a durably fenced notary submission.
"""
from contextlib import closing
import base64
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import plistlib
import posixpath
import re
import sqlite3
import stat
import subprocess
import tarfile
import tempfile
import time

from android_signer_state import canonical, strict_json, private_bytes, private_directory, maintenance

HEX = re.compile(r'[a-f0-9]{64}')
UUID = re.compile(r'[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}')
TEAM = re.compile(r'[A-Z0-9]{10}')
TOOLS = {'codesign', 'productbuild', 'notarytool', 'stapler', 'pkgutil', 'spctl', 'security', 'openssl'}
PROFILE = {'projectId', 'goalId', 'target', 'bundleId', 'expectedTeamId', 'expectedSigner',
           'expectedApplicationSigner', 'applicationCertificateFile', 'installerCertificateFile',
           'keychainFile', 'entitlementsFile', 'notaryProfile', 'tools'}
REQUEST = {'protocolVersion', 'action', 'operationKey', 'projectId', 'goalId', 'taskId', 'planId',
           'requirementRevision', 'signingInputDigest', 'sourceBindingDigest', 'runId', 'repositoryId',
           'commit', 'target', 'artifactId', 'artifactDigest', 'artifactSize', 'artifactPath',
           'expectedSigner', 'expectedApplicationSigner', 'expectedTeamId'}
OBSERVATION = {'notarizationId', 'requestBinding', 'payloadDigest'}
MAX_ARCHIVE = 256 << 20
MAX_EXPANDED = 768 << 20
MACHO = {bytes.fromhex(v) for v in ['feedface', 'cefaedfe', 'feedfacf', 'cffaedfe', 'cafebabe', 'bebafeca', 'cafebabf', 'bfbafeca']}


def fields(value, expected):
    if not isinstance(value, dict) or set(value) != expected:
        raise ValueError('missing or unexpected signing fields')


def digest(value):
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def regular_hash(path, limit=MAX_ARCHIVE, allow_empty=False):
    path = Path(path)
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or not (0 if allow_empty else 1) <= info.st_size <= limit or info.st_nlink != 1:
            raise ValueError('bounded regular signing file required')
        h, size = hashlib.sha256(), 0
        while data := stream.read(1 << 20):
            h.update(data); size += len(data)
            if size > limit: raise ValueError('signing file exceeds limit')
        if size != info.st_size: raise ValueError('signing file changed during readback')
    return h.hexdigest(), size


def write_private(path, body):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'wb') as stream:
        stream.write(body); stream.flush(); os.fsync(stream.fileno())
    sync_directory(Path(path).parent)


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try: os.fsync(descriptor)
    finally: os.close(descriptor)


def load_config(path):
    config = strict_json(private_bytes(Path(path).absolute(), 1 << 20))
    fields(config, {'stateDir', 'profiles'})
    if not isinstance(config['stateDir'], str) or not Path(config['stateDir']).is_absolute():
        raise ValueError('absolute private state directory required')
    root = Path(config['stateDir'])
    if root.exists() or root.is_symlink(): private_directory(root)
    if not isinstance(config['profiles'], list) or not 1 <= len(config['profiles']) <= 100:
        raise ValueError('fixed signing profiles required')
    seen = set()
    for profile in config['profiles']:
        fields(profile, PROFILE)
        if not all(isinstance(value, str) for key, value in profile.items() if key != 'tools'):
            raise ValueError('fixed signing identities must be strings')
        key = (profile['projectId'], profile['goalId'], profile['target'])
        if (key in seen or profile['target'] != 'macos' or not TEAM.fullmatch(profile['expectedTeamId'])
                or not HEX.fullmatch(profile['expectedSigner']) or not HEX.fullmatch(profile['expectedApplicationSigner'])
                or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9.-]{1,200}\.[A-Za-z0-9-]+', profile['bundleId'])
                or not re.fullmatch(r'[A-Za-z0-9_.-]{1,100}', profile['notaryProfile'])):
            raise ValueError('invalid or duplicate macOS signing profile')
        seen.add(key)
        for identity, prefix in [('projectId', 'project_'), ('goalId', 'goal_')]:
            if not re.fullmatch(prefix + r'[A-Za-z0-9_-]{1,100}', profile[identity]):
                raise ValueError('invalid fixed profile scope')
        for name, limit in [('applicationCertificateFile', 1 << 20), ('installerCertificateFile', 1 << 20),
                            ('keychainFile', 64 << 20), ('entitlementsFile', 65536)]:
            body = private_bytes(Path(profile[name]), limit)
            if Path(profile[name]).resolve().is_relative_to(root.resolve()):
                raise ValueError('credentials must remain outside signing state')
            expected = {'applicationCertificateFile': 'expectedApplicationSigner', 'installerCertificateFile': 'expectedSigner'}.get(name)
            if expected and hashlib.sha256(body).hexdigest() != profile[expected]:
                raise ValueError('pinned DER certificate differs from the fixed identity')
            if name == 'entitlementsFile':
                try:
                    body.decode('ascii'); entitlements = plistlib.loads(body)
                except (UnicodeError, plistlib.InvalidFileException, ValueError) as error:
                    raise ValueError('administrator XML ASCII entitlements required') from error
                if (not body.startswith(b'<?xml') or not isinstance(entitlements, dict) or len(entitlements) > 32
                        or any(not isinstance(k, str) or not k.startswith('com.apple.') or type(v) is not bool for k, v in entitlements.items())
                        or entitlements.get('com.apple.security.get-task-allow') or entitlements.get('get-task-allow')):
                    raise ValueError('debug or unsupported entitlements are forbidden')
        fields(profile['tools'], TOOLS)
        for name in TOOLS:
            tool = profile['tools'][name]
            if not isinstance(tool, str) or not Path(tool).is_absolute() or not Path(tool).is_file() or not os.access(tool, os.X_OK):
                raise ValueError('absolute administrator Apple executable required')
    return config


def decode_request(body):
    if len(body) > 65536: raise ValueError('signing request exceeds limit')
    request = strict_json(body)
    validate_request(request)
    return request


def validate_request(request):
    status = isinstance(request, dict) and request.get('action') == 'status'
    fields(request, REQUEST | OBSERVATION if status else REQUEST)
    if (not all(isinstance(v, str) for k, v in request.items() if k not in {'artifactSize', 'requirementRevision'})
            or type(request['artifactSize']) is not int or not 0 < request['artifactSize'] <= MAX_ARCHIVE
            or type(request['requirementRevision']) is not int or request['requirementRevision'] < 1
            or request['protocolVersion'] != 'macos-1' or request['action'] not in {'sign', 'status'} or request['target'] != 'macos'
            or request['operationKey'] != 'sign-' + request['taskId'] or not re.fullmatch(r'[a-f0-9]{40}', request['commit'])
            or not TEAM.fullmatch(request['expectedTeamId'])
            or any(not HEX.fullmatch(request[k]) for k in ['artifactDigest', 'expectedSigner', 'expectedApplicationSigner', 'signingInputDigest', 'sourceBindingDigest'])):
        raise ValueError('invalid authoritative native signing request')
    for name, prefix in [('projectId', 'project_'), ('goalId', 'goal_'), ('runId', 'run_'), ('taskId', 'task_'),
                          ('planId', 'plan_'), ('repositoryId', 'repository_'), ('artifactId', 'artifact_')]:
        if not re.fullmatch(prefix + r'[A-Za-z0-9_-]{1,100}', request[name]): raise ValueError('invalid signing request identity')
    if status:
        if request['artifactPath'] or not UUID.fullmatch(request['notarizationId']) or not HEX.fullmatch(request['requestBinding']) or not HEX.fullmatch(request['payloadDigest']):
            raise ValueError('status must reference the known operation without an input path')
    elif not Path(request['artifactPath']).is_absolute(): raise ValueError('absolute input archive required')


def path_parts(name):
    if (not isinstance(name, str) or len(name) > 1024 or name.startswith('/') or '\\' in name
            or any(not re.fullmatch(r'[A-Za-z0-9_ .@+()=-]{1,200}', p) or p in {'.', '..'} or p.startswith('._') for p in name.split('/'))):
        raise ValueError('unsafe or unsupported APP archive path')
    return name.split('/')


def inspect_archive(path, destination, platform='macos'):
    if platform not in {'macos', 'ios'}:
        raise ValueError('unsupported native application platform')
    try:
        return _inspect_archive(path, destination, platform)
    except (tarfile.TarError, EOFError) as error:
        raise ValueError('invalid or truncated APP archive') from error


def _inspect_archive(path, destination, platform):
    """Stream to a private empty directory; never use extractall or execute code."""
    regular_hash(path)
    destination = Path(destination)
    destination.mkdir(mode=0o700)
    private_directory(destination)
    nodes, explicit, root, total, count = {}, set(), None, 0, 0
    with tarfile.open(path, 'r|*') as archive:
        for member in archive:
            count += 1
            if count > 50000: raise ValueError('APP archive entry limit exceeded')
            name = member.name.rstrip('/') if member.isdir() else member.name
            parts = path_parts(name)
            if root is None:
                root = parts[0]
                if not root.endswith('.app') or root == '.app': raise ValueError('one application root required')
            if parts[0] != root: raise ValueError('one exact application root required')
            key = name.casefold()
            if key in explicit or (key in nodes and nodes[key]['path'] != name): raise ValueError('duplicate or case-ambiguous APP member')
            if member.mode & 0o7000 or member.issparse(): raise ValueError('special APP mode or sparse member forbidden')
            for index in range(1, len(parts)):
                parent = '/'.join(parts[:index]); old = nodes.get(parent.casefold())
                if old and (old['path'] != parent or old['type'] != 'dir'): raise ValueError('ambiguous APP parent')
                nodes.setdefault(parent.casefold(), {'path': parent, 'type': 'dir', 'mode': 0o755})
                (destination / parent).mkdir(mode=0o755, exist_ok=True)
                (destination / parent).chmod(0o755)
            if key in nodes and (nodes[key]['type'] != 'dir' or not member.isdir()): raise ValueError('APP parent changed type')
            explicit.add(key)
            target = destination / name
            if member.isdir():
                target.mkdir(mode=0o755, exist_ok=True); target.chmod(0o755); node = {'path': name, 'type': 'dir', 'mode': 0o755}
            elif member.type in {tarfile.REGTYPE, tarfile.AREGTYPE}:
                total += member.size
                if member.size < 0 or member.size > MAX_ARCHIVE or total > MAX_EXPANDED: raise ValueError('APP expansion limit exceeded')
                mode = 0o755 if member.mode & 0o111 else 0o644
                descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
                os.fchmod(descriptor, mode)
                h, size = hashlib.sha256(), 0
                with os.fdopen(descriptor, 'wb') as output, archive.extractfile(member) as source:
                    while data := source.read(1 << 20): output.write(data); h.update(data); size += len(data)
                if size != member.size: raise ValueError('truncated APP member')
                node = {'path': name, 'type': 'file', 'mode': mode, 'sha256': h.hexdigest()}
            elif member.issym():
                link = member.linkname
                if not isinstance(link, str) or not link or len(link) > 1024 or link.startswith('/') or '\\' in link:
                    raise ValueError('invalid APP symbolic link')
                for part in link.split('/'):
                    if part not in {'.', '..'}: path_parts(part)
                node = {'path': name, 'type': 'link', 'mode': 0o777, 'link': link}
            else: raise ValueError('hard links and special APP members are forbidden')
            signature_root = [root,'Contents','_CodeSignature'] if platform == 'macos' else [root,'_CodeSignature']
            if parts[:len(signature_root)] == signature_root or parts[-1] in {'embedded.provisionprofile', 'embedded.mobileprovision'}:
                raise ValueError('top-level APP must be unsigned and free of provisioning profiles')
            if ('_CodeSignature' in parts or parts[-1]=='CodeResources') and not any(part.endswith(('.framework','.app','.xpc','.appex','.bundle')) for part in parts[1:-1]):
                raise ValueError('signature metadata outside an embedded native bundle')
            nodes[key] = node
    if not root or not nodes: raise ValueError('empty APP archive')

    def resolve(name, seen=()):
        if len(seen) > 64: raise ValueError('APP link resolution limit exceeded')
        parts = PurePosixPath(name).parts
        for index in range(1, len(parts) + 1):
            prefix = '/'.join(parts[:index]); node = nodes.get(prefix.casefold())
            if not node or node['path'] != prefix: raise ValueError('APP link points to a missing or aliased member')
            if node['type'] == 'link':
                if prefix in seen: raise ValueError('APP symbolic link cycle')
                next_path = posixpath.normpath(posixpath.join(posixpath.dirname(prefix), node['link'], *parts[index:]))
                if not next_path.startswith(root + '/'): raise ValueError('APP link escapes its application')
                return resolve(next_path, (*seen, prefix))
            if index < len(parts) and node['type'] != 'dir': raise ValueError('APP link traverses a file')
        return nodes[name.casefold()]

    for node in nodes.values():
        if node['type'] == 'link': resolve(node['path'])
    for node in nodes.values():
        if node['type'] == 'link': (destination / node['path']).symlink_to(node['link'])
    app = destination / root
    info_path = app / ('Contents/Info.plist' if platform == 'macos' else 'Info.plist')
    if info_path.is_symlink() or not info_path.is_file() or info_path.stat().st_size > 1 << 20: raise ValueError('regular APP Info.plist required')
    try: info = plistlib.loads(info_path.read_bytes())
    except (ValueError, plistlib.InvalidFileException) as error: raise ValueError('invalid APP property list') from error
    executable = info.get('CFBundleExecutable') if isinstance(info, dict) else None
    expected_platform = 'MacOSX' if platform == 'macos' else 'iPhoneOS'
    if (not isinstance(info, dict) or info.get('CFBundlePackageType') != 'APPL' or info.get('CFBundleSupportedPlatforms') != [expected_platform]
            or (platform == 'ios' and info.get('DTPlatformName') != 'iphoneos')
            or not isinstance(executable, str) or not re.fullmatch(r'[A-Za-z0-9_ .+-]{1,200}', executable) or executable in {'.','..'}
            or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9.-]{1,200}\.[A-Za-z0-9-]+', str(info.get('CFBundleIdentifier', '')))
            or not isinstance(info.get('CFBundleShortVersionString'),str) or not isinstance(info.get('CFBundleVersion'),str)
            or not re.fullmatch(r'[0-9]+(?:\.[0-9]+){0,2}', info.get('CFBundleShortVersionString', ''))
            or not re.fullmatch(r'[0-9]+(?:\.[0-9]+){0,2}', info.get('CFBundleVersion', ''))):
        raise ValueError('native APP metadata or main executable is invalid')
    code = []
    for node in nodes.values():
        if node['type'] != 'file': continue
        path = destination / node['path']
        with path.open('rb') as stream: magic = stream.read(4)
        if magic in MACHO:
            if path.stat().st_size < 28: raise ValueError('truncated native executable')
            code.append(node['path'])
        elif node['mode'] & 0o111 or node['path'].endswith('.dylib'):
            raise ValueError('unsigned native profile forbids executable scripts or foreign binaries')
    main = root + ('/Contents/MacOS/' if platform == 'macos' else '/') + executable
    if main not in code or not nodes[main.casefold()]['mode'] & 0o111: raise ValueError('native Mach-O main executable required')
    bundles = {root}
    for name in code:
        for parent in PurePosixPath(name).parents:
            if str(parent).endswith(('.app', '.framework', '.xpc', '.appex', '.bundle')): bundles.add(str(parent))
    records = sorted(nodes.values(), key=lambda node: node['path'])
    return {'app': str(app), 'bundleId': info['CFBundleIdentifier'], 'version': info['CFBundleShortVersionString'], 'build': info['CFBundleVersion'],
            'payloadDigest': digest(records), 'nodes': records, 'code': code,
            'signables': sorted(set(code) | bundles, key=lambda name: (-len(PurePosixPath(name).parts), name))}


class AppleTools:
    def __init__(self, profile): self.profile = profile

    def run(self, tool, directory, *args):
        # Explicit keychain arguments are constructed by this module. No inherited
        # environment, shell, repository configuration or default credential lookup.
        directory = Path(directory)
        env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'HOME': str(directory), 'TMPDIR': str(directory), 'LANG': 'C', 'LC_ALL': 'C'}
        with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as error:
            # Stay in the Worker's private process group: creating a new session
            # here would let Apple tools survive cancellation of the signer.
            process = subprocess.Popen([self.profile['tools'][tool], *map(str, args)], cwd=directory, env=env,
                                       stdout=out, stderr=error)
            started = time.monotonic()
            try:
                while process.poll() is None:
                    if time.monotonic() - started > 120 or os.fstat(out.fileno()).st_size > 2 << 20 or os.fstat(error.fileno()).st_size > 2 << 20:
                        raise ValueError('Apple tool timeout or output limit exceeded')
                    time.sleep(0.02)
            except BaseException:
                # Never signal an inherited shell group when invoked by an
                # administrator. The Worker also reaps the private task group.
                try: process.kill()
                except ProcessLookupError: pass
                process.wait(); raise
            if os.fstat(out.fileno()).st_size > 2 << 20 or os.fstat(error.fileno()).st_size > 2 << 20:
                raise ValueError('Apple tool output limit exceeded')
            out.seek(0); error.seek(0)
            result = process.returncode, out.read().decode(errors='replace'), error.read().decode(errors='replace')
        logs = directory / 'diagnostics'; logs.mkdir(mode=0o700, exist_ok=True); private_directory(logs)
        descriptor, name = tempfile.mkstemp(prefix=tool + '-', suffix='.json', dir=logs)
        with os.fdopen(descriptor, 'w') as stream:
            json.dump({'tool': tool, 'exitCode': result[0], 'stdout': result[1], 'stderr': result[2]}, stream)
            stream.flush(); os.fsync(stream.fileno())
        return result


class Signer:
    def __init__(self, config, tools=None):
        self.config, self.injected_tools = config, tools

    def profile(self, request):
        matches = [p for p in self.config['profiles'] if all(p[k] == request[k] for k in ['projectId','goalId','target','expectedSigner','expectedApplicationSigner','expectedTeamId'])]
        if len(matches) != 1: raise ValueError('no exact administrator signing profile')
        return matches[0]

    def database(self):
        root = Path(self.config['stateDir']); root.mkdir(mode=0o700, parents=True, exist_ok=True); private_directory(root)
        path = root / 'macos-operations.sqlite'
        if path.exists() or path.is_symlink(): private_bytes(path, 128 << 20)
        else: write_private(path, b'')
        db = sqlite3.connect(path, timeout=5)
        db.execute('PRAGMA synchronous=FULL'); db.execute('PRAGMA journal_mode=DELETE')
        db.execute('CREATE TABLE IF NOT EXISTS operations (key TEXT PRIMARY KEY, binding TEXT NOT NULL, status TEXT NOT NULL, request TEXT NOT NULL, result TEXT NOT NULL, updated TEXT NOT NULL)')
        db.commit()
        return db

    def save(self, db, request, binding, status, data):
        db.execute('INSERT INTO operations VALUES(?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET status=excluded.status,result=excluded.result,updated=excluded.updated',
                   (request['operationKey'], binding, status, canonical(request), canonical(data), str(time.time())))
        db.commit(); sync_directory(Path(self.config['stateDir']))

    def inspect(self, key):
        if not re.fullmatch(r'sign-task_[A-Za-z0-9_-]{1,100}', key): raise ValueError('invalid operation key')
        with maintenance(self.config, exclusive=True), closing(self.database()) as db:
            row = db.execute('SELECT binding,status,result FROM operations WHERE key=?', (key,)).fetchone()
            if not row: raise ValueError('unknown signing operation')
            state = strict_json(row[2])
            return {'operationKey': key, 'requestBinding': row[0], 'status': row[1], 'notarizationId': state.get('notarizationId',''),
                    'submittedDigest': state.get('submittedDigest',''), 'artifactDigest': state.get('receipt',{}).get('artifactDigest','')}

    def handle(self, request):
        validate_request(request)
        profile = self.profile(request)
        stable = {k: v for k, v in request.items() if k not in {'runId','action','artifactPath'} | OBSERVATION}
        profile_binding = digest({'profile': profile, 'entitlementsSHA256': hashlib.sha256(private_bytes(Path(profile['entitlementsFile']),65536)).hexdigest()})
        binding = digest({'request': stable, 'profileBinding': profile_binding})
        with maintenance(self.config, exclusive=True), closing(self.database()) as db:
            row = db.execute('SELECT binding,status,result FROM operations WHERE key=?', (request['operationKey'],)).fetchone()
            opdir = Path(self.config['stateDir']) / ('op-' + hashlib.sha256(request['operationKey'].encode()).hexdigest())
            if row:
                if row[0] != binding: raise ValueError('operation request or administrator configuration changed')
                status, data = row[1], strict_json(row[2])
                private_directory(opdir)
                if request['action'] == 'status' and any(request[k] != (binding if k=='requestBinding' else data.get(k)) for k in OBSERVATION):
                    raise ValueError('status observation differs from the original operation')
                if status == 'signed':
                    receipt = data['receipt']
                    actual, size = regular_hash(receipt['artifactPath'])
                    if actual != receipt['artifactDigest'] or size != receipt['artifactSize']: raise ValueError('completed PKG readback changed')
                    return receipt
                if status not in {'notary_submitted','staple_intent'}: raise ValueError('uncertain or rejected operation requires administrator reconciliation')
                if time.time() - data['createdAt'] > 86400 or data.get('polls',0) >= 288: raise ValueError('private notarization deadline or query limit reached')
                data['polls'] = data.get('polls',0) + 1
                self.save(db, stable, binding, status, data)
                tools = self.injected_tools or AppleTools(profile)
                return self.poll(db, stable, binding, profile, tools, opdir, status, data)
            if request['action'] != 'sign': raise ValueError('status cannot create or resubmit a signing operation')
            body = private_bytes(Path(request['artifactPath']), MAX_ARCHIVE)
            if len(body) != request['artifactSize'] or hashlib.sha256(body).hexdigest() != request['artifactDigest']:
                raise ValueError('input archive differs from the authoritative digest')
            opdir.mkdir(mode=0o700); sync_directory(opdir.parent)
            snapshot = opdir / 'unsigned.tar.gz'; write_private(snapshot, body)
            del body
            extracted = opdir / 'application'
            metadata = inspect_archive(snapshot, extracted)
            if metadata['bundleId'] != profile['bundleId']: raise ValueError('bundle identifier differs from the administrator profile')
            data = {k: metadata[k] for k in ['bundleId','version','build','payloadDigest']}
            data.update(createdAt=time.time(),polls=0,profileBinding=profile_binding)
            self.save(db, stable, binding, 'signing_intent', data)
            tools = self.injected_tools or AppleTools(profile)
            app_identity, installer_identity = self.identities(profile, tools, opdir)
            self.sign_application(profile, tools, opdir, metadata, app_identity)
            submitted = opdir / 'submitted.pkg'
            code, _, _ = tools.run('productbuild', opdir, '--component', metadata['app'], '/Applications', '--sign', installer_identity,
                                    '--keychain', profile['keychainFile'], '--timestamp', submitted)
            if code: raise ValueError('fixed productbuild did not confirm signed PKG')
            submitted.chmod(0o600)
            self.verify_pkg(profile,tools,opdir,submitted,notarized=False)
            package_digest, package_size = regular_hash(submitted)
            data.update(submittedDigest=package_digest,submittedSize=package_size)
            self.save(db, stable, binding, 'notary_submit_intent', data)
            code, text, _ = tools.run('notarytool', opdir, 'submit', submitted, '--keychain-profile', profile['notaryProfile'],
                                     '--keychain', profile['keychainFile'], '--output-format', 'json')
            if code: raise ValueError('notarization submission outcome is unknown; do not resubmit')
            response = strict_json(text)
            identifier = response.get('id','').lower() if isinstance(response,dict) and isinstance(response.get('id'),str) else ''
            if not UUID.fullmatch(identifier): raise ValueError('notarization submission did not confirm an identifier')
            data['notarizationId'] = identifier
            self.save(db, stable, binding, 'notary_submitted', data)
            return self.receipt(stable,binding,data)

    def identities(self, profile, tools, directory):
        identities = []
        for file_key, expected, kind in [('applicationCertificateFile','expectedApplicationSigner','Application'),('installerCertificateFile','expectedSigner','Installer')]:
            body = private_bytes(Path(profile[file_key]),1<<20)
            if hashlib.sha256(body).hexdigest() != profile[expected]: raise ValueError('fixed DER certificate changed')
            code, text, _ = tools.run('openssl', directory, 'x509', '-inform','DER','-in',profile[file_key],'-noout','-subject','-nameopt','sep_multiline,sname,utf8','-checkend','0')
            subjects = dict(re.findall(r'^\s*(CN|OU)\s*=\s*(.+?)\s*$',text,re.M))
            cn, team = subjects.get('CN',''),subjects.get('OU','')
            if code or team != profile['expectedTeamId'] or not cn.startswith('Developer ID '+kind+': ') or not cn.endswith(' ('+team+')'):
                raise ValueError('certificate is not the pinned Developer ID type and team')
            identities.append((hashlib.sha1(body).hexdigest(),cn))
        code, text, _ = tools.run('security',directory,'find-certificate','-a','-p',profile['keychainFile'])
        if code: raise ValueError('fixed private keychain certificates unavailable')
        certificates = [base64.b64decode(re.sub(r'\s','',value),validate=True) for value in re.findall(r'-----BEGIN CERTIFICATE-----\s*([A-Za-z0-9+/=\s]+?)\s*-----END CERTIFICATE-----',text)]
        for expected, (_,cn) in zip(['expectedApplicationSigner','expectedSigner'],identities):
            if sum(hashlib.sha256(body).hexdigest()==profile[expected] for body in certificates)!=1: raise ValueError('exact pinned certificate must exist once in the private keychain')
        return identities[0][0],identities[1][1]

    def sign_application(self, profile, tools, directory, metadata, identity):
        extracted = Path(metadata['app']).parent
        code, text, error = tools.run('codesign',directory,'--display','--verbose=4',metadata['app'])
        if code!=1 or 'code object is not signed at all' not in text+error:
            raise ValueError('top-level native APP must be confirmed unsigned')
        for name in metadata['code']:
            code,text,error = tools.run('codesign',directory,'--display',str(extracted/name))
            if code==0:
                # SDK frameworks may already carry a seal. Verify it before
                # replacing it with the administrator's pinned application ID.
                code,_,_=tools.run('codesign',directory,'--verify','--strict',str(extracted/name))
                if code: raise ValueError('embedded executable signature is invalid')
            elif code!=1 or 'code object is not signed at all' not in text+error:
                raise ValueError('embedded signature state is unconfirmed')
        for index,name in enumerate(metadata['signables']):
            path = extracted/name
            code, _, _ = tools.run('codesign',directory,'--force','--sign',identity,'--keychain',profile['keychainFile'],
                                    '--options','runtime','--timestamp','--entitlements',profile['entitlementsFile'],path)
            if code: raise ValueError('fixed Developer ID application signing failed')
            prefix = str(directory/('certificate-'+str(index)+'-'))
            code,text,error = tools.run('codesign',directory,'--display','--verbose=4','--extract-certificates',prefix,path)
            text += error
            if (code or hashlib.sha256(Path(prefix+'0').read_bytes()).hexdigest()!=profile['expectedApplicationSigner']
                    or 'TeamIdentifier='+profile['expectedTeamId'] not in text or '(runtime)' not in text
                    or not re.search(r'^Timestamp=(?!none\s*$).+',text,re.M)
                    or 'Authority=Developer ID Application:' not in text):
                raise ValueError('signed code differs from the pinned hardened Developer ID application')
        code, _, _ = tools.run('codesign',directory,'--verify','--deep','--strict','--verbose=2',metadata['app'])
        if code: raise ValueError('signed APP verification failed')
        expected = {node['path']:node for node in metadata['nodes']}
        observed = set()
        for current, directories, files in os.walk(metadata['app'],followlinks=False):
            for name in [*directories,*files]:
                path=Path(current)/name; relative=path.relative_to(extracted).as_posix()
                if '_CodeSignature' in PurePosixPath(relative).parts: continue
                observed.add(relative)
                node=expected.get(relative)
                if node is None: raise ValueError('signing added an unexpected application resource')
                mode=path.lstat().st_mode
                if node['type']=='link':
                    if not stat.S_ISLNK(mode) or os.readlink(path)!=node['link']: raise ValueError('application link changed during signing')
                elif node['type']=='dir':
                    if not stat.S_ISDIR(mode): raise ValueError('application directory changed during signing')
                elif not stat.S_ISREG(mode) or stat.S_IMODE(mode)!=node['mode'] or (relative not in metadata['code'] and regular_hash(path,allow_empty=True)[0]!=node['sha256']):
                    raise ValueError('ordinary application resource changed during signing')
        comparable={name for name in expected if '_CodeSignature' not in PurePosixPath(name).parts}
        if observed != comparable-{Path(metadata['app']).name}: raise ValueError('application resource was removed during signing')

    def verify_pkg(self,profile,tools,directory,path,notarized):
        code,text,error=tools.run('pkgutil',directory,'--check-signature',path)
        text+=error
        fingerprint=re.search(r'SHA256 Fingerprint:\s*((?:[A-Fa-f0-9]{2}[\s:]*){32})',text)
        if (code or 'Status: signed by a developer certificate issued by Apple for distribution' not in text
                or not re.search(r'^\s*1\. Developer ID Installer: .+ \('+re.escape(profile['expectedTeamId'])+r'\)\s*$',text,re.M)
                or not fingerprint or re.sub('[^a-fA-F0-9]','',fingerprint.group(1)).lower()!=profile['expectedSigner']):
            raise ValueError('PKG does not have the pinned trusted Developer ID Installer signature')
        if notarized:
            code,text,error=tools.run('spctl',directory,'--assess','--type','install','--verbose=2',path)
            if code or not re.search(r'^source=Notarized Developer ID\s*$',text+error,re.M): raise ValueError('Gatekeeper did not accept the notarized PKG')

    def receipt(self,request,binding,data):
        return {'protocolVersion':'macos-1','state':'processing','operationKey':request['operationKey'],'requestBinding':binding,
                'payloadDigest':data['payloadDigest'],'certificateSHA256':request['expectedSigner'],
                'applicationCertificateSHA256':request['expectedApplicationSigner'],'teamId':request['expectedTeamId'],
                'notarizationId':data['notarizationId'],'inputArtifactId':request['artifactId'],'inputArtifactDigest':request['artifactDigest']}

    def poll(self,db,request,binding,profile,tools,directory,status,data):
        submitted=directory/'submitted.pkg'
        actual,size=regular_hash(submitted)
        if actual!=data['submittedDigest'] or size!=data['submittedSize']: raise ValueError('original submitted PKG changed')
        final=directory/'stapled.pkg'
        if status=='notary_submitted':
            code,text,_=tools.run('notarytool',directory,'info',data['notarizationId'],'--keychain-profile',profile['notaryProfile'],
                                  '--keychain',profile['keychainFile'],'--output-format','json')
            if code: return self.receipt(request,binding,data)
            info=strict_json(text)
            if not isinstance(info,dict) or str(info.get('id','')).lower()!=data['notarizationId']: raise ValueError('notary info returned a foreign job')
            if info.get('status')=='In Progress': return self.receipt(request,binding,data)
            if info.get('status') in {'Invalid','Rejected'}:
                self.save(db,request,binding,'notary_rejected',data);raise ValueError('Apple rejected notarization')
            if info.get('status')!='Accepted': raise ValueError('unknown notarization status requires reconciliation')
            descriptor,name=tempfile.mkstemp(prefix='notary-log-',suffix='.json',dir=directory);os.close(descriptor);Path(name).unlink()
            code,_,_=tools.run('notarytool',directory,'log',data['notarizationId'],name,'--keychain-profile',profile['notaryProfile'],'--keychain',profile['keychainFile'])
            if code: return self.receipt(request,binding,data)
            log_path=Path(name);log_path.chmod(0o600);log=strict_json(private_bytes(log_path,2<<20))
            if (not isinstance(log,dict) or str(log.get('jobId','')).lower()!=data['notarizationId'] or log.get('status')!='Accepted'
                    or str(log.get('sha256','')).lower()!=data['submittedDigest']
                    or (log.get('issues') is not None and (not isinstance(log['issues'],list) or any(not isinstance(v,dict) or v.get('severity')=='error' for v in log['issues'])))):
                raise ValueError('notary log does not prove the original PKG was accepted')
            data['acceptedLogDigest']=regular_hash(log_path,2<<20)[0]
            data['acceptedLogName']=log_path.name
            self.save(db,request,binding,'staple_intent',data)
            write_private(final,private_bytes(submitted,MAX_ARCHIVE))
            code,_,_=tools.run('stapler',directory,'staple',final)
            if code: raise ValueError('stapling outcome is unconfirmed; only read-only recovery is permitted')
        log_name=data.get('acceptedLogName','')
        if not re.fullmatch(r'notary-log-[A-Za-z0-9_-]+\.json',log_name): raise ValueError('original accepted log is missing')
        log_body=private_bytes(directory/log_name,2<<20)
        if hashlib.sha256(log_body).hexdigest()!=data.get('acceptedLogDigest'): raise ValueError('original accepted notary log changed')
        if not final.is_file() or final.is_symlink(): raise ValueError('stapled PKG missing; no automatic mutation retry')
        code,_,_=tools.run('stapler',directory,'validate',final)
        if code: raise ValueError('stapled ticket not confirmed; administrator reconciliation required')
        self.verify_pkg(profile,tools,directory,final,notarized=True)
        final_digest,final_size=regular_hash(final)
        objects=Path(self.config['stateDir'])/'objects';objects.mkdir(mode=0o700,exist_ok=True);private_directory(objects)
        destination=objects/(final_digest+'.pkg')
        if destination.exists() or destination.is_symlink():
            if regular_hash(destination)!=(final_digest,final_size): raise ValueError('occupied PKG object differs from notarized bytes')
        else: write_private(destination,private_bytes(final,MAX_ARCHIVE))
        receipt=self.receipt(request,binding,data)
        receipt.update(state='complete',artifactPath=str(destination),artifactDigest=final_digest,artifactSize=final_size,notarized=True,
                       bundleId=data['bundleId'],version=data['version'],build=data['build'])
        data['receipt']=receipt
        self.save(db,request,binding,'signed',data)
        return receipt
