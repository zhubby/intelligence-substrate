"""Private POSIX signer-state coordination; not an Agent or network interface."""
from contextlib import contextmanager, closing
import ctypes
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import stat
import sys


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True)


def strict_json(body):
    def pairs(items):
        result, seen = {}, set()
        for key, value in items:
            if key.casefold() in seen:
                raise ValueError('ambiguous private recovery metadata')
            seen.add(key.casefold())
            result[key] = value
        return result
    try:
        return json.loads(body, object_pairs_hook=pairs,
                          parse_constant=lambda value: (_ for _ in ()).throw(ValueError('non-finite recovery metadata')))
    except (UnicodeError, json.JSONDecodeError) as error:
        raise ValueError('invalid private recovery metadata') from error


def private_directory(path):
    path = Path(path)
    try:
        info = path.lstat()
    except OSError as error:
        raise ValueError('an owned private directory is required') from error
    if not path.is_absolute() or not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError('an owned private directory is required')
    return path


@contextmanager
def private_stream(path, limit, allow_empty=False):
    path = Path(path)
    if not path.is_absolute():
        raise ValueError('an absolute private regular file is required')
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except OSError as error:
        raise ValueError('an owned private regular file is required') from error
    with os.fdopen(descriptor, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077
                or info.st_nlink != 1 or not (0 if allow_empty else 1) <= info.st_size <= limit):
            raise ValueError('an owned bounded private regular file is required')
        yield stream


def private_bytes(path, limit):
    with private_stream(path, limit) as stream:
        expected = os.fstat(stream.fileno()).st_size
        body = stream.read(limit + 1)
        if len(body) != expected:
            raise ValueError('private file changed during snapshot')
        return body


def file_record(path, limit=256 << 20):
    digest, total = hashlib.sha256(), 0
    with private_stream(path, limit) as stream:
        expected = os.fstat(stream.fileno()).st_size
        while body := stream.read(1 << 20):
            total += len(body)
            if total > limit:
                raise ValueError('private file exceeds snapshot bound')
            digest.update(body)
        if total != expected:
            raise ValueError('private file changed during snapshot')
    return {'sha256': digest.hexdigest(), 'size': total, 'mode': 0o600}


def safe_relative(name):
    if (not isinstance(name, str) or len(name) > 240 or name.startswith('/')
            or any(not re.fullmatch(r'[A-Za-z0-9_.-]{1,120}', part) or part in {'.', '..'} for part in name.split('/'))):
        raise ValueError('unsafe private archive path')
    return name


def inside(root, path):
    root, path = Path(root), Path(path)
    try:
        name = safe_relative(str(path.relative_to(root)))
    except ValueError as error:
        raise ValueError('private input must remain inside the explicit vault root') from error
    current = root
    for part in Path(name).parts[:-1]:
        current = private_directory(current / part)
    return name


@contextmanager
def maintenance(config, exclusive=False):
    if os.name != 'posix':
        raise ValueError('private signing maintenance requires POSIX')
    import fcntl
    root = Path(config['stateDir'])
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    private_directory(root)
    descriptor = os.open(root / '.maintenance.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_nlink != 1:
            raise ValueError('unsafe private maintenance lock')
        try:
            fcntl.flock(descriptor, (fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH) | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError('signer busy or held for maintenance; stop dispatch before retry') from error
        yield
    finally:
        os.close(descriptor)


def journal_rows(path):
    file_record(path)
    with closing(sqlite3.connect(Path(path).as_uri() + '?mode=ro', uri=True, timeout=5)) as db:
        db.execute('PRAGMA query_only=ON')
        if db.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
            raise ValueError('private signing journal integrity failed')
        schema = db.execute("SELECT name,type FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").fetchall()
        columns = [row[1] for row in db.execute('PRAGMA table_info(operations)')]
        if schema != [('operations', 'table')] or columns != ['key', 'binding', 'status', 'request', 'result', 'updated']:
            raise ValueError('unsupported private signing journal schema')
        rows = db.execute('SELECT key,binding,status,request,result,updated FROM operations ORDER BY key LIMIT 100001').fetchall()
        if len(rows) > 100000 or not all(all(isinstance(value, str) for value in row) for row in rows):
            raise ValueError('invalid or oversized private signing journal')
    return rows


def rows_digest(rows):
    return hashlib.sha256(canonical(rows).encode()).hexdigest()


def recovery_guard(config, request):
    marker = Path(config['stateDir']) / 'recovery-fence.json'
    if not marker.exists() and not marker.is_symlink():
        return
    try:
        fence = strict_json(private_bytes(marker, 4 << 20))
        expected = {'version', 'mode', 'originalRoot', 'statePath', 'configDigest', 'journalDigest', 'files'}
        if (not isinstance(fence, dict) or set(fence) != expected or fence['version'] != 1
                or fence['mode'] != 'successful-replay-only'
                or hashlib.sha256(canonical(config).encode()).hexdigest() != fence['configDigest']):
            raise ValueError('restored configuration differs from the recovery fence')
        root = private_directory(Path(fence['originalRoot']))
        state_path = safe_relative(fence['statePath'])
        if Path(config['stateDir']) != root / state_path or not isinstance(fence['files'], dict):
            raise ValueError('restored state differs from the recovery fence')
        for name, record in fence['files'].items():
            safe_relative(name)
            if name != state_path + '/operations.sqlite':
                if file_record(root / inside(root, root / name)) != record:
                    raise ValueError('restored private bytes differ from the recovery fence')
        rows = journal_rows(Path(config['stateDir']) / 'operations.sqlite')
        if rows_digest(rows) != fence['journalDigest']:
            raise ValueError('restored signing journal differs from the recovery fence')
        found = [row for row in rows if row[0] == request.get('operationKey')]
        if len(found) != 1 or found[0][2] != 'signed':
            raise ValueError('recovery permits successful known-operation replay only; reconciliation required')
    except (OSError, TypeError, KeyError, sqlite3.Error) as error:
        raise ValueError('private recovery fence validation failed') from error


def publish_directory(staging, destination):
    """An atomic no-replace directory move; no racy exists/rename fallback."""
    staging, destination = Path(staging), Path(destination)
    private_directory(staging)
    private_directory(destination.parent)
    if not destination.is_absolute() or staging.parent != destination.parent:
        raise ValueError('recovery publication requires a private same-parent staging directory')
    library = ctypes.CDLL(None, use_errno=True)
    if sys.platform == 'darwin':
        rename = library.renamex_np
        rename.argtypes, rename.restype = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint], ctypes.c_int
        result = rename(os.fsencode(staging), os.fsencode(destination), 0x4)  # RENAME_EXCL
    elif sys.platform.startswith('linux'):
        rename = library.renameat2
        rename.argtypes, rename.restype = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint], ctypes.c_int
        result = rename(-100, os.fsencode(staging), -100, os.fsencode(destination), 1)  # RENAME_NOREPLACE
    else:
        raise ValueError('exclusive recovery publication requires Linux or macOS')
    if result:
        raise ValueError('recovery destination is occupied or exclusive publication failed')
    descriptor = os.open(destination.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
