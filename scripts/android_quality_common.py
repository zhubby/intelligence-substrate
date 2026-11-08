"""Shared exact-APK/source/owned-install helpers for trusted Android quality runners."""
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import stat
import subprocess
import tempfile
import zipfile


def sha(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as stream:
        while block := stream.read(1024 * 1024):
            digest.update(block)
    return digest.hexdigest()


def validate_apk(path):
    if Path(path).stat().st_size > 256 * 1024 * 1024:
        raise ValueError('APK exceeds diagnostic size limit')
    with zipfile.ZipFile(path) as archive:
        seen, total = {}, 0
        members = archive.infolist()
        if len(members) > 20000:
            raise ValueError('too many APK entries')
        for member in members:
            name = member.filename.rstrip('/') if member.is_dir() else member.filename
            parts = name.split('/')
            if len(name) > 1024 or any(not re.fullmatch(r'[A-Za-z0-9_.@+$-]{1,200}', p) or p in {'.', '..'} for p in parts):
                raise ValueError('unsafe APK path')
            mode = member.external_attr >> 16
            if stat.S_ISLNK(mode) or stat.S_IFMT(mode) not in {0, stat.S_IFREG, stat.S_IFDIR} or member.flag_bits & 1:
                raise ValueError('special or encrypted APK entry')
            key = name.casefold()
            old = seen.get(key)
            if old and (old[0] != name or old[1] != 'implicit' or not member.is_dir()):
                raise ValueError('duplicate, ambiguous or replaced APK entry')
            for i in range(1, len(parts)):
                parent = '/'.join(parts[:i])
                previous = seen.get(parent.casefold())
                if previous and (previous[0] != parent or previous[1] == 'file'):
                    raise ValueError('ambiguous APK parent')
                seen.setdefault(parent.casefold(), (parent, 'implicit'))
            seen[key] = (name, 'directory' if member.is_dir() else 'file')
            total += member.file_size
            if member.file_size > 100 * 1024 * 1024 or total > 400 * 1024 * 1024:
                raise ValueError('APK expansion limit exceeded')
        names = {m.filename for m in members if not m.is_dir()}
        if not {'AndroidManifest.xml', 'classes.dex', 'lib/arm64-v8a/libflutter.so'} <= names or not any(n.startswith('assets/flutter_assets/') for n in names):
            raise ValueError('missing diagnostic Flutter APK structure')
        if archive.testzip() is not None:
            raise ValueError('APK CRC mismatch')
    return {'sha256': sha(path), 'entries': len(members), 'uncompressedBytes': total,
            'verification': 'ZIP safety and diagnostic Flutter structure only'}


def source_snapshot(root, relative=Path()):
    files = {}
    for p in sorted((root / relative).iterdir()):
        name = p.relative_to(root).as_posix()
        if p.name in {'.git', '.dart_tool', 'build', '.cache'} or p.name.startswith('.intelligence-'):
            continue
        if relative == Path() and p.name in {'quality', 'artifacts'} or name == 'Library/Application Support/rtk':
            continue
        if p.is_symlink():
            raise ValueError('linked source is unsupported')
        if p.is_dir():
            files.update(source_snapshot(root, p.relative_to(root)))
        elif p.is_file():
            files[name] = sha(p)
        else:
            raise ValueError('special source entry')
    return files


@contextmanager
def device_lock(serial, port, directory=None):
    directory = directory or Path(tempfile.gettempdir()) / 'intelligence-android-quality-locks'
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    if directory.is_symlink() or directory.stat().st_uid != os.getuid():
        raise ValueError('unsafe device lock directory')
    fd = os.open(directory / (str(port) + '-' + serial + '.lock'), os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or os.fstat(fd).st_uid != os.getuid():
            raise ValueError('unsafe device lock file')
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        os.close(fd)


def require_free_port(port):
    with socket.socket() as probe:
        probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        probe.bind(('127.0.0.1', port))


def package_path(device, package):
    try:
        return device('shell', 'pm', 'path', package).strip()
    except subprocess.CalledProcessError as error:
        # The real Android pm tool returns1 with no output for an absent app.
        # Transport errors and all diagnostic output must still fail closed.
        if error.returncode == 1 and not error.stdout and not error.stderr:
            return ''
        raise


def require_absent_package(device, package):
    if package_path(device, package):
        raise ValueError('target package already installed; refuse replacing or uninstalling it')


def installed_apk_digest(device, package):
    paths = package_path(device, package).splitlines()
    if len(paths) != 1 or not re.fullmatch(r'package:/data/app/[A-Za-z0-9_./+=~-]+/base\.apk', paths[0]):
        raise ValueError('unexpected installed APK path')
    line = device('exec-out', 'sha256sum', paths[0][8:]).strip()
    digest = line.split()[0] if line else ''
    if not re.fullmatch(r'[a-f0-9]{64}', digest):
        raise ValueError('installed APK digest unavailable')
    return digest


def install_owned_package(device, apk, state):
    # If install succeeds but its reply is lost, cleanup still reconciles only
    # this initially absent package and verifies its bytes before uninstalling.
    state['installAttempted'] = True
    device('install', '-t', str(apk))


def prepare_install_apk(source, destination):
    # Worker artifact names are content addressed and have no .apk extension;
    # adb install rejects those names before opening their valid APK bytes.
    with source.open('rb') as origin, destination.open('xb') as target:
        shutil.copyfileobj(origin, target)
    if sha(source) != sha(destination):
        raise ValueError('APK install copy differs from the platform artifact')


def validate_signature(signature, badging, expected=None):
    if expected is None:
        if 'CN=Android Debug' not in signature:
            raise ValueError('diagnostic APK requires an Android Debug signer')
        return {'kind': 'development-debug-key'}
    certificates = re.findall(r'^Signer #(\d+) certificate SHA-256 digest: ([a-fA-F0-9]{64})$', signature, re.M)
    if (not re.fullmatch(r'[a-f0-9]{64}', expected) or certificates != [('1', expected)]
            or 'cn=android debug' in signature.lower() or 'application-debuggable' in badging
            or 'Verified using v2 scheme (APK Signature Scheme v2): true' not in signature
            or 'Verified using v3 scheme (APK Signature Scheme v3): true' not in signature):
        raise ValueError('release APK requires the exact non-debug v2/v3 certificate and non-debuggable build')
    return {'kind': 'explicit-release-certificate', 'certificateSHA256': expected}

def command(argv, **options):
    proc = subprocess.Popen(['rtk', 'proxy', *map(str, argv)], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, start_new_session=True, **options)
    try:
        stdout, stderr = proc.communicate(timeout=60)
    except BaseException:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        proc.communicate()
        raise
    if proc.returncode:
        raise subprocess.CalledProcessError(proc.returncode, argv, stdout, stderr)
    return stdout
