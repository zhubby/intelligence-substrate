#!/usr/bin/env python3
"""Validate every Web archive entry before extracting any bytes or running tools."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tarfile
import tempfile

SEGMENT = re.compile(r'[A-Za-z0-9_.@+-]+\Z')
DEVICE = re.compile(r'(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])\Z', re.I)


def bounded_tar(artifact, max_bytes, max_files):
    # Bound gzip output before tarfile can allocate an attacker-sized PAX header.
    raw = tempfile.TemporaryFile()
    try:
        limit = max_bytes + max_files * 4096 + 1024 * 1024
        with gzip.open(artifact, 'rb') as stream:
            while block := stream.read(min(1024 * 1024, limit + 1)):
                limit -= len(block)
                if limit < 0:
                    raise ValueError('decompression limit exceeded')
                raw.write(block)
        raw.seek(0)
        return raw
    except BaseException:
        raw.close()
        raise


def extract(artifact, destination, max_files=20000, max_bytes=400 * 1024 * 1024):
    if max_files < 1 or max_files > 20000 or max_bytes < 1 or max_bytes > 400 * 1024 * 1024:
        raise ValueError('invalid extraction limits')
    # Do not resolve away a destination link, even in an ancestor.
    destination = destination.absolute()
    for part in [destination, *destination.parents]:
        if part.is_symlink():
            raise ValueError('linked destination')
    if destination.exists():
        raise ValueError('destination already exists')
    digest = hashlib.sha256()
    with artifact.open('rb') as stream:
        while data := stream.read(1024 * 1024):
            digest.update(data)
    with bounded_tar(artifact, max_bytes, max_files) as raw, tarfile.open(fileobj=raw, mode='r:') as archive:
        members, seen, total = [], {}, 0
        for member in archive:
            name = member.name
            parts = name.split('/')
            if (len(name) > 1024 or any(not SEGMENT.fullmatch(p) or len(p) > 200 or p in ('.', '..')
                    or p.endswith('.') or DEVICE.fullmatch(p.split('.')[0]) for p in parts)):
                raise ValueError('unsafe entry path')
            if not (member.isfile() or member.isdir()) or member.issparse():
                raise ValueError('links and special entries are forbidden')
            key = name.casefold()
            if key in seen and seen[key][1] != 'implicit':
                raise ValueError('duplicate or case-colliding entry')
            # Include implicit parents, so A/x and a/y cannot merge on macOS.
            for index in range(1, len(parts)):
                parent = '/'.join(parts[:index])
                previous = seen.get(parent.casefold())
                if previous and (previous[0] != parent or previous[1] == 'file'):
                    raise ValueError('ambiguous parent or file used as directory')
                seen.setdefault(parent.casefold(), (parent, 'implicit'))
            previous = seen.get(key)
            if previous and (previous[0] != name or previous[1] != 'implicit' or not member.isdir()):
                raise ValueError('entry replaces an existing parent')
            seen[key] = (name, 'directory' if member.isdir() else 'file')
            total += member.size
            if member.size < 0 or member.size > 100 * 1024 * 1024 or total > max_bytes or len(members) >= max_files:
                raise ValueError('archive expansion limit exceeded')
            members.append(member)
        files = {m.name: m for m in members if m.isfile()}
        for name in ['index.html', 'main.dart.js', 'flutter_bootstrap.js']:
            if name not in files:
                raise ValueError('missing Flutter entry: ' + name)
        if files['index.html'].size > 1024 * 1024 or files['main.dart.js'].size < 100000:
            raise ValueError('unexpected Flutter entry size')
        entry = archive.extractfile(files['index.html']).read()
        if b'flutter_bootstrap.js' not in entry or b'<base href="/">' not in entry:
            raise ValueError('unexpected Flutter entry structure')
        # Structural checks do not certify compilation; actual browser tests do that.
        destination.parent.mkdir(parents=True, exist_ok=True)
        temporary = Path(tempfile.mkdtemp(prefix='.web-archive-', dir=destination.parent))
        try:
            for member in members:
                output = temporary.joinpath(*member.name.split('/'))
                if member.isdir():
                    output.mkdir(parents=True, exist_ok=True)
                else:
                    output.parent.mkdir(parents=True, exist_ok=True)
                    with archive.extractfile(member) as source, output.open('xb') as target:
                        shutil.copyfileobj(source, target)
            os.rename(temporary, destination)
        finally:
            if temporary.exists():
                shutil.rmtree(temporary)
    return {'sha256': digest.hexdigest(), 'files': len(files), 'entries': len(members), 'bytes': total,
            'verification': 'safe extraction and Flutter entry structure only'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--artifact', type=Path, required=True)
    parser.add_argument('--destination', type=Path, required=True)
    parser.add_argument('--max-files', type=int, default=20000)
    parser.add_argument('--max-bytes', type=int, default=400 * 1024 * 1024)
    args = parser.parse_args()
    try:
        print(json.dumps(extract(args.artifact, args.destination, args.max_files, args.max_bytes)))
    except (OSError, ValueError, tarfile.TarError) as error:
        print('invalid Web archive: ' + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
