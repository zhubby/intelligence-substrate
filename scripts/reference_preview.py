"""Package the isolated Flutter/Go preview profile; never modify build output."""
import hashlib
import json
from pathlib import Path
import re


def entries(built, backend, mode, arch):
    if mode not in ('feedback', 'booking') or arch not in ('arm64', 'amd64'):
        raise ValueError('unsupported preview runtime')
    if backend.is_symlink() or not backend.is_file():
        raise ValueError('preview backend must be a regular file')
    binary = backend.read_bytes()
    if not 0 < len(binary) <= 32 << 20:
        raise ValueError('preview backend size exceeds limit')
    manifest = {'schemaVersion': 1, 'profile': 'flutter-go-v1', 'mode': mode, 'goarch': arch,
                'backendSHA256': hashlib.sha256(binary).hexdigest(), 'backendSize': len(binary)}
    for path in sorted(built.rglob('*')):
        name = path.relative_to(built).as_posix()
        if path.is_symlink() or not (path.is_file() or path.is_dir()):
            raise ValueError('unsafe preview entry')
        if name == '.last_build_id':
            continue
        if any(p.startswith('.') for p in Path(name).parts) or name.split('/')[0] == 'substrate-preview':
            raise ValueError('unexpected hidden or reserved preview entry')
        if path.is_dir():
            continue
        body = path.read_bytes()
        if name == 'index.html':
            body = re.sub(rb'<base\s+href="/"\s*/?>', b'', body)
            if re.search(rb'<base\b', body, re.I):
                raise ValueError('unsupported preview base element')
        yield name, body
    yield 'substrate-preview/manifest.json', json.dumps(manifest, sort_keys=True).encode()
    yield 'substrate-preview/backend', binary
