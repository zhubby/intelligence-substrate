#!/usr/bin/env python3
"""Administrator-owned, offline Android APK signing; never runs repository tools."""
import argparse
from contextlib import closing
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import sqlite3
import stat
import subprocess
import sys
import tempfile
import zipfile
import android_bundle as bundle
import android_signer_state as signing_state

TARGETS = {'android-google-play', 'android-huawei', 'android-xiaomi', 'android-oppo', 'android-vivo'}
REQUEST_FIELDS = {'protocolVersion', 'action', 'operationKey', 'projectId', 'goalId', 'runId',
                  'repositoryId', 'commit', 'target', 'artifactId', 'artifactDigest', 'artifactSize',
                  'artifactPath', 'expectedSigner'}
PROFILE_FIELDS = {'projectId', 'goalId', 'target', 'bundleId', 'expectedSigner', 'keystoreFile',
                  'keyAlias', 'storePasswordFile', 'keyPasswordFile', 'aaptPath', 'apkSignerPath',
                  'zipalignPath', 'javaHome'}
AAB_PROFILE_FIELDS = (PROFILE_FIELDS - {'aaptPath', 'apkSignerPath', 'zipalignPath'}) | {'bundletoolPath', 'bundletoolSHA256', 'bundleVerifierPath'}
HEX = re.compile(r'[a-f0-9]{64}')
IDENTITY = re.compile(r'[a-z]+_[A-Za-z0-9_-]{1,100}')


def sha(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as stream:
        while data := stream.read(1024 * 1024):
            digest.update(data)
    return digest.hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True)


def strict_json(data):
    def pairs(items):
        result, seen = {}, set()
        for key, value in items:
            if key.casefold() in seen:
                raise ValueError('duplicate or case-ambiguous JSON key')
            result[key] = value
            seen.add(key.casefold())
        return result
    try:
        return json.loads(data, object_pairs_hook=pairs, parse_constant=lambda value: (_ for _ in ()).throw(ValueError('non-finite JSON value')))
    except (UnicodeError, json.JSONDecodeError) as error:
        raise ValueError('one valid JSON object is required') from error


def fields(value, expected):
    if not isinstance(value, dict) or set(value) != expected:
        raise ValueError('missing, unknown or case-aliased fields')


def private_file(value, limit):
    if not isinstance(value, str) or not Path(value).is_absolute():
        raise ValueError('an absolute private file is required')
    path = Path(value)
    mode = path.lstat()
    if not stat.S_ISREG(mode.st_mode) or mode.st_uid != os.getuid() or mode.st_mode & 0o077 or not 0 < mode.st_size <= limit:
        raise ValueError('private files must be owned regular files with private permissions')
    return path


def load_config(path):
    path = private_file(str(path), 1 << 20)
    config = strict_json(path.read_bytes())
    fields(config, {'stateDir', 'profiles'})
    if not isinstance(config['stateDir'], str) or not Path(config['stateDir']).is_absolute():
        raise ValueError('absolute private state directory required')
    root = Path(config['stateDir'])
    if root.exists() or root.is_symlink():
        mode = root.lstat()
        if not stat.S_ISDIR(mode.st_mode) or mode.st_uid != os.getuid() or mode.st_mode & 0o077:
            raise ValueError('state directory must be private and owned')
    if not isinstance(config['profiles'], list) or not 1 <= len(config['profiles']) <= 100:
        raise ValueError('one or more fixed signing profiles required')
    seen = set()
    for profile in config['profiles']:
        aab = isinstance(profile, dict) and profile.get('target') == 'android-google-play'
        fields(profile, AAB_PROFILE_FIELDS if aab else PROFILE_FIELDS)
        key = tuple(profile.get(k) for k in ['projectId', 'goalId', 'target'])
        if (not all(isinstance(v, str) for v in profile.values()) or key in seen
                or profile['target'] not in TARGETS or not HEX.fullmatch(profile['expectedSigner'])
                or not re.fullmatch(r'[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+', profile['bundleId'])
                or not re.fullmatch(r'[A-Za-z0-9_.-]{1,100}', profile['keyAlias'])
                or not profile['projectId'].startswith('project_') or not IDENTITY.fullmatch(profile['projectId'])
                or not profile['goalId'].startswith('goal_') or not IDENTITY.fullmatch(profile['goalId'])):
            raise ValueError('invalid or duplicate fixed Android signing profile')
        seen.add(key)
        for name, limit in [('keystoreFile', 16 << 20), ('storePasswordFile', 4096), ('keyPasswordFile', 4096)]:
            secret = private_file(profile[name], limit)
            if secret.resolve().is_relative_to(root.resolve()):
                raise ValueError('private keys and passwords must remain outside the signing state directory')
        if aab:
            bundle.configuration(profile)
            jarsigner = Path(profile['javaHome']) / 'bin/jarsigner'
            if not jarsigner.is_file() or not os.access(jarsigner, os.X_OK):
                raise ValueError('administrator jarsigner is unavailable')
        else:
            for name in ['aaptPath', 'apkSignerPath', 'zipalignPath', 'javaHome']:
                if not Path(profile[name]).is_absolute():
                    raise ValueError('absolute administrator SDK and Java paths required')
            for name in ['aaptPath', 'apkSignerPath', 'zipalignPath']:
                tool = Path(profile[name])
                if not tool.is_file() or not os.access(tool, os.X_OK):
                    raise ValueError('administrator SDK executable is unavailable')
    return config


def decode_request(data):
    if len(data) > 65536:
        raise ValueError('signing input exceeds the protocol limit')
    request = strict_json(data)
    fields(request, REQUEST_FIELDS)
    validate_request(request)
    return request


def validate_request(request):
    fields(request, REQUEST_FIELDS)
    if (not all(isinstance(v, str) for k, v in request.items() if k != 'artifactSize')
            or type(request['artifactSize']) is not int or not 0 < request['artifactSize'] <= 256 << 20
            or request['protocolVersion'] != '1' or request['action'] != 'sign'
            or not re.fullmatch(r'[A-Za-z0-9_.-]{1,160}', request['operationKey'])
            or request['target'] not in TARGETS or not HEX.fullmatch(request['artifactDigest'])
            or not HEX.fullmatch(request['expectedSigner']) or not re.fullmatch(r'[a-f0-9]{40}', request['commit'])
            or not Path(request['artifactPath']).is_absolute()):
        raise ValueError('invalid exact signing request')
    for key, prefix in [('projectId', 'project_'), ('goalId', 'goal_'), ('runId', 'run_'),
                        ('repositoryId', 'repository_'), ('artifactId', 'artifact_')]:
        if not request[key].startswith(prefix) or not IDENTITY.fullmatch(request[key]):
            raise ValueError('invalid signing request identity')


def validate_archive(path, allow_signed=False):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 256 << 20:
        raise ValueError('APK must be a bounded regular file')
    files, entries, total = {}, {}, 0
    with zipfile.ZipFile(path) as archive:
        members = archive.infolist()
        if len(members) > 20000:
            raise ValueError('too many APK entries')
        if not allow_signed and archive.start_dir >= 16:
            with path.open('rb') as stream:
                stream.seek(archive.start_dir - 16)
                if stream.read(16) == b'APK Sig Block 42':
                    raise ValueError('APK already contains a signing block')
        for member in members:
            name = member.filename.rstrip('/') if member.is_dir() else member.filename
            parts = name.split('/')
            if len(name) > 1024 or any(not re.fullmatch(r'[A-Za-z0-9_.@+$-]{1,200}', p) or p in {'.', '..'} for p in parts):
                raise ValueError('unsafe APK entry path')
            mode = member.external_attr >> 16
            if stat.S_IFMT(mode) not in {0, stat.S_IFREG, stat.S_IFDIR} or member.flag_bits & 1:
                raise ValueError('linked, special or encrypted APK entry')
            key = name.casefold()
            previous = entries.get(key)
            if previous and (previous[0] != name or previous[1] != 'implicit' or not member.is_dir()):
                raise ValueError('duplicate or ambiguous APK entries')
            for i in range(1, len(parts)):
                parent = '/'.join(parts[:i])
                old = entries.get(parent.casefold())
                if old and (old[0] != parent or old[1] == 'file'):
                    raise ValueError('ambiguous APK parent')
                entries.setdefault(parent.casefold(), (parent, 'implicit'))
            entries[key] = (name, 'dir' if member.is_dir() else 'file')
            if not allow_signed and re.fullmatch(r'META-INF/[^/]+\.(SF|RSA|DSA|EC)', name, re.I):
                raise ValueError('APK already contains a JAR signature')
            total += member.file_size
            if member.file_size > 100 << 20 or total > 400 << 20:
                raise ValueError('APK expansion limit exceeded')
            if not member.is_dir():
                digest = hashlib.sha256()
                with archive.open(member) as stream:
                    while data := stream.read(1024 * 1024):
                        digest.update(data)
                files[name] = digest.hexdigest()
    if not {'AndroidManifest.xml', 'classes.dex', 'lib/arm64-v8a/libflutter.so'} <= set(files) or not any(n.startswith('assets/flutter_assets/') for n in files):
        raise ValueError('missing native Flutter APK structure')
    return {'files': files, 'payloadDigest': hashlib.sha256(canonical(files).encode()).hexdigest(),
            'entries': len(members), 'uncompressedBytes': total}


def sdk(profile, directory, tool, *args, capture=True):
    env = {'PATH': os.environ.get('PATH', ''), 'JAVA_HOME': profile['javaHome'],
           'HOME': str(directory), 'TMPDIR': str(directory), 'LC_ALL': 'C', 'LANG': 'C'}
    with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as error:
        process = subprocess.Popen([profile[tool], *map(str, args)], cwd=directory, env=env,
            stdout=out if capture else subprocess.DEVNULL, stderr=error if capture else subprocess.DEVNULL,
            start_new_session=True)
        try:
            process.wait(timeout=120)
        except BaseException:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait()
            raise
        if out.tell() > 1 << 20 or error.tell() > 1 << 20:
            raise ValueError('Android SDK diagnostic output exceeded the limit')
        out.seek(0)
        error.seek(0)
        return process.returncode, out.read().decode(errors='replace'), error.read().decode(errors='replace')


def metadata(profile, directory, path):
    code, text, _ = sdk(profile, directory, 'aaptPath', 'dump', 'badging', path)
    identity = re.findall(r"^package: name='([^']+)' versionCode='([0-9]+)' versionName='([^']+)'", text, re.M)
    minimum = re.findall(r"^sdkVersion:'([0-9]+)'", text, re.M)
    if (code or len(identity) != 1 or identity[0][0] != profile['bundleId'] or len(minimum) != 1
            or not 1 <= int(identity[0][1]) <= 2100000000 or int(minimum[0]) < 24
            or 'application-debuggable' in text or "native-code: 'arm64-v8a'" not in text):
        raise ValueError('release APK identity, version, SDK, ABI or debuggable state is invalid')
    return {'packageName': identity[0][0], 'versionCode': int(identity[0][1]),
            'versionName': identity[0][2], 'minSDK': int(minimum[0])}


def database(config, readonly=False):
    path = Path(config['stateDir']) / 'operations.sqlite'
    if path.is_symlink() or path.exists() and not path.is_file():
        raise ValueError('unsafe signing journal path')
    if readonly:
        if not path.exists():
            raise ValueError('signing operation is unknown')
        return sqlite3.connect(path.as_uri() + '?mode=ro', uri=True, timeout=20)
    Path(config['stateDir']).mkdir(mode=0o700, parents=True, exist_ok=True)
    connection = sqlite3.connect(path, timeout=20, isolation_level=None)
    path.chmod(0o600)
    connection.execute('PRAGMA journal_mode=WAL')
    connection.execute('PRAGMA synchronous=FULL')
    connection.execute('CREATE TABLE IF NOT EXISTS operations (key TEXT PRIMARY KEY,binding TEXT NOT NULL,status TEXT NOT NULL,request TEXT NOT NULL,result TEXT NOT NULL DEFAULT \'\',updated TEXT NOT NULL)')
    return connection


def inspect(config, key):
    if not isinstance(key, str) or not re.fullmatch(r'[A-Za-z0-9_.-]{1,160}', key):
        raise ValueError('invalid signing operation key')
    with closing(database(config, readonly=True)) as db:
        row = db.execute('SELECT binding,status,request,result,updated FROM operations WHERE key=?', (key,)).fetchone()
    if row is None:
        raise ValueError('signing operation is unknown')
    return {'operationKey': key, 'requestBinding': row[0], 'status': row[1],
            'request': json.loads(row[2]), 'result': json.loads(row[3]) if row[3] else None, 'updatedAt': row[4]}


def execute(config, request):
    validate_request(request)
    with signing_state.maintenance(config):
        signing_state.recovery_guard(config, request)
        return execute_locked(config, request)


def execute_locked(config, request):
    validate_request(request)
    profiles = [p for p in config['profiles'] if all(p[k] == request[k] for k in ['projectId', 'goalId', 'target', 'expectedSigner'])]
    if len(profiles) != 1:
        raise ValueError('request does not match one fixed signing profile')
    profile = profiles[0]
    original = Path(request['artifactPath'])
    if original.is_symlink() or not original.is_file() or original.stat().st_size != request['artifactSize'] or sha(original) != request['artifactDigest']:
        raise ValueError('input APK bytes differ from the exact artifact binding')
    aab = request['target'] == 'android-google-play'
    extension = '.aab' if aab else '.apk'
    archive = bundle.archive(original) if aab else validate_archive(original)
    public_request = {k: v for k, v in request.items() if k != 'artifactPath'}
    binding = hashlib.sha256(canonical({'request': public_request, 'profile': profile}).encode()).hexdigest()
    root = Path(config['stateDir'])
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.signing-check-', dir=root) as temp:
        directory = Path(temp)
        if aab:
            identity = bundle.inspect(profile, directory, original)
        else:
            identity = metadata(profile, directory, original)
            code, _, diagnostic = sdk(profile, directory, 'apkSignerPath', 'verify', '--verbose', original)
            if code != 1 or 'Missing META-INF/MANIFEST.MF' not in diagnostic:
                raise ValueError('release APK is signed or its unsigned state is not established')
    db = database(config)
    try:
        db.execute('BEGIN IMMEDIATE')
        row = db.execute('SELECT binding,status,result FROM operations WHERE key=?', (request['operationKey'],)).fetchone()
        if row:
            db.commit()
            if row[0] != binding:
                raise ValueError('operation key is already bound to a different signing binding')
            if row[1] != 'signed':
                raise ValueError('started or failed signing intent requires operator reconciliation')
            result = json.loads(row[2])
            stored = Path(result['artifactPath'])
            if (stored.is_symlink() or not stored.is_file() or stored.stat().st_size != result['artifactSize']
                    or sha(stored) != result['artifactDigest'] or result['certificateSHA256'] != request['expectedSigner']
                    or result['payloadDigest'] != archive['payloadDigest'] or result['requestBinding'] != binding):
                raise ValueError('persisted signed object or receipt failed integrity verification')
            return result
        stamp = datetime.now(timezone.utc).isoformat()
        db.execute('INSERT INTO operations(key,binding,status,request,updated) VALUES(?,?,?,?,?)',
                   (request['operationKey'], binding, 'signing', canonical(public_request), stamp))
        db.commit()
        try:
            with tempfile.TemporaryDirectory(prefix='.signing-', dir=root) as temp, tempfile.TemporaryDirectory(prefix='is-signing-secrets-') as secrets_temp:
                directory = Path(temp)
                staged = directory / ('input' + extension)
                with original.open('rb') as source, staged.open('xb') as target:
                    while data := source.read(1024 * 1024):
                        target.write(data)
                if sha(staged) != request['artifactDigest']:
                    raise ValueError('input changed before trusted signing')
                signed = directory / ('signed' + extension)
                if not aab:
                    aligned = directory / 'aligned.apk'
                    if sdk(profile, directory, 'zipalignPath', '-P', '16', '-f', '4', staged, aligned)[0]:
                        raise ValueError('Android SDK alignment failed')
                # apksigner caches a password-file stream by its path. Two
                # options naming one file consume two lines, even when both
                # passwords are the same. Independent private copies also keep
                # credential bytes outside the durable journal/object root.
                passwords = {}
                for key in ['storePasswordFile', 'keyPasswordFile']:
                    private_file(profile[key], 4096)
                    with os.fdopen(os.open(profile[key], os.O_RDONLY | os.O_NOFOLLOW), 'rb') as source:
                        value = source.read(4097)
                    if not 0 < len(value) <= 4096:
                        raise ValueError('private password input changed during signing')
                    password_path = Path(secrets_temp) / key
                    with os.fdopen(os.open(password_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'wb') as password:
                        password.write(value)
                    passwords[key] = str(password_path)
                if aab:
                    java_profile = {**profile, 'jarsignerPath': str(Path(profile['javaHome']) / 'bin/jarsigner')}
                    code, _, _ = sdk(java_profile, directory, 'jarsignerPath', '-keystore', profile['keystoreFile'],
                        '-storepass:file', passwords['storePasswordFile'], '-keypass:file', passwords['keyPasswordFile'],
                        '-digestalg', 'SHA-256', '-sigalg', 'SHA256withRSA', '-sigfile', 'UPLOAD',
                        '-signedjar', signed, staged, profile['keyAlias'], capture=False)
                    if code:
                        raise ValueError('trusted AAB signing failed; inspect the persisted intent')
                    after = bundle.archive(signed, signed=True)
                    if bundle.inspect(profile, directory, signed, request['expectedSigner']) != identity or after['files'] != archive['files']:
                        raise ValueError('signed AAB payload or identity differs from the exact unsigned input')
                else:
                    code, _, _ = sdk(profile, directory, 'apkSignerPath', 'sign', '--ks', profile['keystoreFile'],
                        '--ks-key-alias', profile['keyAlias'], '--ks-pass', 'file:' + passwords['storePasswordFile'],
                        '--key-pass', 'file:' + passwords['keyPasswordFile'], '--v1-signing-enabled', 'false',
                        '--v2-signing-enabled', 'true', '--v3-signing-enabled', 'true', '--v4-signing-enabled', 'false',
                        '--out', signed, aligned, capture=False)
                    if code:
                        raise ValueError('trusted Android SDK signing failed; inspect the persisted intent')
                    code, signing, _ = sdk(profile, directory, 'apkSignerPath', 'verify', '--verbose', '--print-certs', signed)
                    certs = re.findall(r'^Signer #(\d+) certificate SHA-256 digest: ([a-fA-F0-9]{64})$', signing, re.M)
                    if (code or certs != [('1', request['expectedSigner'])]
                            or 'cn=android debug' in signing.lower()
                            or 'Verified using v2 scheme (APK Signature Scheme v2): true' not in signing
                            or 'Verified using v3 scheme (APK Signature Scheme v3): true' not in signing):
                        raise ValueError('signed APK does not have the exact approved v2/v3 release certificate')
                    if sdk(profile, directory, 'zipalignPath', '-c', '-P', '16', '4', signed)[0]:
                        raise ValueError('signed APK page alignment failed')
                    after = validate_archive(signed, allow_signed=True)
                    if after['files'] != archive['files'] or metadata(profile, directory, signed) != identity:
                        raise ValueError('signed APK payload differs from the approved unsigned input')
                digest = sha(signed)
                destination = root / (digest + extension)
                try:
                    os.link(signed, destination)
                    destination.chmod(0o600)
                except FileExistsError:
                    if destination.is_symlink() or not destination.is_file() or sha(destination) != digest:
                        raise ValueError('content-addressed signed object is occupied or corrupt')
                with destination.open('rb') as stream:
                    os.fsync(stream.fileno())
                descriptor = os.open(root, os.O_RDONLY)
                try:
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
                result = {**identity, 'operationKey': request['operationKey'], 'requestBinding': binding,
                    'artifactPath': str(destination), 'artifactDigest': digest, 'artifactSize': destination.stat().st_size,
                    'certificateSHA256': request['expectedSigner'], 'payloadDigest': after['payloadDigest'],
                    'inputArtifactId': request['artifactId'], 'inputArtifactDigest': request['artifactDigest']}
                db.execute('UPDATE operations SET status=?,result=?,updated=? WHERE key=? AND binding=? AND status=?',
                    ('signed', canonical(result), datetime.now(timezone.utc).isoformat(), request['operationKey'], binding, 'signing'))
                return result
        except BaseException:
            db.execute('UPDATE operations SET status=?,updated=? WHERE key=? AND binding=? AND status=?',
                ('reconciliation_required', datetime.now(timezone.utc).isoformat(), request['operationKey'], binding, 'signing'))
            raise
    finally:
        db.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--inspect', metavar='OPERATION_KEY')
    args = parser.parse_args()
    def cancel(signum, frame):
        signal.signal(signum, signal.SIG_IGN)
        raise InterruptedError('trusted signing interrupted; inspect the persisted operation')
    for sig in [signal.SIGINT, signal.SIGTERM]:
        signal.signal(sig, cancel)
    try:
        config = load_config(args.config)
        if args.inspect:
            result = inspect(config, args.inspect)
        else:
            request = decode_request(sys.stdin.buffer.read(65537))
            result = execute(config, request)
        print(canonical(result))
        return 0
    except (Exception, KeyboardInterrupt) as error:
        message = str(error) if isinstance(error, (ValueError, InterruptedError)) else 'trusted signing failed; inspect private state and administrator configuration'
        print(message, file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
