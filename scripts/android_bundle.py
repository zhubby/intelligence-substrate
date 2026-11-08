"""Bounded Flutter AAB inspection with administrator-pinned Java tools."""
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import tempfile
import xml.etree.ElementTree as ET
import zipfile


def sha(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as stream:
        while chunk := stream.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


def signing_metadata(name):
    return bool(re.fullmatch(r'META-INF/(MANIFEST\.MF|[^/]+\.(SF|RSA|DSA|EC))', name, re.I))


def archive(path, signed=False):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or not 0 < path.stat().st_size <= 256 << 20:
        raise ValueError('AAB must be a bounded regular file')
    files, entries, total = {}, {}, 0
    with zipfile.ZipFile(path) as bundle:
        members = bundle.infolist()
        if len(members) > 20000:
            raise ValueError('too many AAB entries')
        for item in members:
            name = item.filename.rstrip('/') if item.is_dir() else item.filename
            parts = name.split('/')
            if len(name) > 1024 or any(not re.fullmatch(r'[A-Za-z0-9_.@+$-]{1,200}', part) or part in {'.', '..'} for part in parts):
                raise ValueError('unsafe AAB entry path')
            if stat.S_IFMT(item.external_attr >> 16) not in {0, stat.S_IFREG, stat.S_IFDIR} or item.flag_bits & 1:
                raise ValueError('linked, special or encrypted AAB entry')
            key = name.casefold()
            old = entries.get(key)
            if old and (old[0] != name or old[1] != 'implicit' or not item.is_dir()):
                raise ValueError('duplicate or ambiguous AAB entry')
            for index in range(1, len(parts)):
                parent = '/'.join(parts[:index])
                old = entries.get(parent.casefold())
                if old and (old[0] != parent or old[1] == 'file'):
                    raise ValueError('ambiguous AAB parent')
                entries.setdefault(parent.casefold(), (parent, 'implicit'))
            entries[key] = (name, 'dir' if item.is_dir() else 'file')
            if not signed and signing_metadata(name) and name.upper() != 'META-INF/MANIFEST.MF':
                raise ValueError('AAB already contains a JAR signature')
            total += item.file_size
            if item.file_size > 100 << 20 or total > 400 << 20:
                raise ValueError('AAB expansion limit exceeded')
            if not item.is_dir():
                digest = hashlib.sha256()
                with bundle.open(item) as stream:
                    while chunk := stream.read(1 << 20):
                        digest.update(chunk)
                if not signing_metadata(name):
                    files[name] = digest.hexdigest()
    required = {'BundleConfig.pb', 'base/manifest/AndroidManifest.xml', 'base/dex/classes.dex', 'base/lib/arm64-v8a/libflutter.so'}
    if not required <= set(files) or not any(n.startswith('base/assets/flutter_assets/') for n in files):
        raise ValueError('missing Flutter AAB structure')
    native_abis = {name.split('/')[2] for name in files if len(name.split('/')) >= 4 and name.split('/')[1] == 'lib'}
    if native_abis != {'arm64-v8a'}:
        raise ValueError('reference AAB must contain only the approved arm64 ABI')
    return {'files': files, 'payloadDigest': hashlib.sha256(json.dumps(files, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode()).hexdigest(),
            'entries': len(members), 'uncompressedBytes': total}


def manifest(text, package_name):
    try:
        if '<!DOCTYPE' in text or '<!ENTITY' in text:
            raise ValueError('AAB manifest entities are forbidden')
        root = ET.fromstring(text)
        android = '{http://schemas.android.com/apk/res/android}'
        versions = [root.get(android + key, '') for key in ['versionCode', 'versionName']]
        sdks, apps = root.findall('uses-sdk'), root.findall('application')
        if (root.tag != 'manifest' or root.get('package') != package_name or len(sdks) != 1 or len(apps) != 1
                or not re.fullmatch(r'[0-9]{1,10}', versions[0]) or not 1 <= int(versions[0]) <= 2100000000
                or not versions[1].strip() or len(versions[1]) > 256
                or not re.fullmatch(r'[0-9]{1,3}', sdks[0].get(android + 'minSdkVersion', ''))
                or not 24 <= int(sdks[0].get(android + 'minSdkVersion')) <= 999
                or apps[0].get(android + 'debuggable') not in {None, 'false'}):
            raise ValueError('AAB release identity, version, SDK or debuggable state is invalid')
        return {'packageName': package_name, 'versionCode': int(versions[0]), 'versionName': versions[1],
                'minSDK': int(sdks[0].get(android + 'minSdkVersion'))}
    except ET.ParseError as error:
        raise ValueError('invalid AAB manifest XML') from error


def configuration(profile):
    for name in ['javaHome', 'bundletoolPath', 'bundleVerifierPath']:
        if not isinstance(profile.get(name), str) or not Path(profile[name]).is_absolute():
            raise ValueError('absolute administrator AAB tool paths required')
    java = Path(profile['javaHome']) / 'bin/java'
    verifier = Path(profile['bundleVerifierPath'])
    jar = Path(profile['bundletoolPath'])
    if (not java.is_file() or not os.access(java, os.X_OK) or not verifier.is_file() or verifier.is_symlink()
            or not jar.is_file() or jar.is_symlink() or jar.stat().st_size > 64 << 20
            or not re.fullmatch(r'[a-f0-9]{64}', profile.get('bundletoolSHA256', ''))
            or sha(jar) != profile['bundletoolSHA256']):
        raise ValueError('administrator AAB tools are unavailable or bundletool digest changed')


def java(profile, directory, *args):
    configuration(profile)
    env = {'PATH': os.environ.get('PATH', ''), 'HOME': str(directory), 'TMPDIR': str(directory), 'LC_ALL': 'C', 'LANG': 'C'}
    with tempfile.TemporaryFile() as output, tempfile.TemporaryFile() as errors:
        process = subprocess.Popen([str(Path(profile['javaHome']) / 'bin/java'), *map(str, args)],
                                   cwd=directory, env=env, stdout=output, stderr=errors, start_new_session=True)
        try:
            process.wait(timeout=120)
        except BaseException:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait()
            raise
        if output.tell() > 1 << 20 or errors.tell() > 1 << 20:
            raise ValueError('AAB diagnostic output exceeds limit')
        output.seek(0)
        if process.returncode:
            raise ValueError('trusted AAB verification failed')
        return output.read().decode()


def inspect(profile, directory, path, expected='unsigned'):
    payload = archive(path, signed=expected != 'unsigned')
    java(profile, directory, '-jar', profile['bundletoolPath'], 'validate', '--bundle=' + str(path))
    xml = java(profile, directory, '-jar', profile['bundletoolPath'], 'dump', 'manifest', '--bundle=' + str(path), '--module=base')
    identity = manifest(xml, profile['bundleId'])
    try:
        verification = json.loads(java(profile, directory, profile['bundleVerifierPath'], path, expected))
        if (set(verification) != {'signed', 'certificateSHA256', 'payloadEntries'}
                or verification['signed'] is not (expected != 'unsigned')
                or verification['certificateSHA256'] != ('' if expected == 'unsigned' else expected)
                or verification['payloadEntries'] != len(payload['files'])):
            raise ValueError('AAB signature does not cover the exact expected payload and certificate')
    except (TypeError, json.JSONDecodeError) as error:
        raise ValueError('invalid AAB signature verifier response') from error
    return identity
