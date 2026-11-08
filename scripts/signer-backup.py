#!/usr/bin/env python3
"""Offline administrator backup/restore of one private Android signing vault."""
from contextlib import closing
from datetime import datetime, timezone
import argparse
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import android_signer_state as state

MAX_TOTAL = 400 << 20
MAX_ARCHIVE = 512 << 20
SECRET_FIELDS = ('keystoreFile', 'storePasswordFile', 'keyPasswordFile')


def signer_module():
    spec = importlib.util.spec_from_file_location('trusted_android_signer', Path(__file__).with_name('sign-android-apk.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def private_write(path, body):
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), 'wb') as stream:
        stream.write(body)
        stream.flush()
        os.fsync(stream.fileno())


def crypto(action, tool, key, source, output):
    tool = Path(tool)
    if not tool.is_absolute() or tool.is_symlink() or not tool.is_file() or not os.access(tool, os.X_OK):
        raise ValueError('an absolute administrator-owned crypto executable is required')
    try:
        result = subprocess.run([str(tool), action, '--key-file', str(key), '--input', str(source), '--output', str(output)],
                                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=120)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise ValueError('private authenticated archive tool failed') from error
    if result.returncode:
        raise ValueError('private authenticated archive tool rejected input or output')


def configuration(root, path, actual_root=None):
    """Validate original profile semantics, mapping only private-file lookups."""
    raw = state.strict_json(state.private_bytes(path, 1 << 20))
    if not isinstance(raw, dict) or set(raw) != {'stateDir', 'profiles'} or not isinstance(raw['profiles'], list):
        raise ValueError('invalid private signer configuration')
    state_name = state.safe_relative(str(Path(raw['stateDir']).relative_to(root)))
    if actual_root is None:
        signer_module().load_config(path)
    else:
        mapped = {'stateDir': str(actual_root / state_name), 'profiles': []}
        for profile in raw['profiles']:
            copy = dict(profile)
            for name in SECRET_FIELDS:
                relative = state.safe_relative(str(Path(profile[name]).relative_to(root)))
                copy[name] = str(actual_root / relative)
            mapped['profiles'].append(copy)
        temporary = actual_root / '.validate-config.json'
        private_write(temporary, state.canonical(mapped).encode())
        try:
            signer_module().load_config(temporary)
        finally:
            temporary.unlink()
    for profile in raw['profiles']:
        for name in SECRET_FIELDS:
            state.safe_relative(str(Path(profile[name]).relative_to(root)))
        for name in ['javaHome', 'aaptPath', 'apkSignerPath', 'zipalignPath', 'bundletoolPath', 'bundleVerifierPath']:
            if name in profile and Path(profile[name]).resolve().is_relative_to(root.resolve()):
                raise ValueError('administrator tools must be installed separately from the private vault')
    return raw, state_name


def validate_journal(config, original_root, actual_root, state_name, records):
    module = signer_module()
    rows = state.journal_rows(actual_root / state_name / 'operations.sqlite')
    for key, binding, status, request_body, result_body, updated in rows:
        request = module.strict_json(request_body)
        module.validate_request({**request, 'artifactPath': '/private/independent-recovery-input'})
        if set(request) != module.REQUEST_FIELDS - {'artifactPath'} or request['operationKey'] != key:
            raise ValueError('private journal request differs from its operation key')
        profiles = [p for p in config['profiles'] if all(p[k] == request[k] for k in ['projectId', 'goalId', 'target', 'expectedSigner'])]
        if len(profiles) != 1 or hashlib.sha256(module.canonical({'request': request, 'profile': profiles[0]}).encode()).hexdigest() != binding:
            raise ValueError('private journal profile binding differs from the approved request')
        if status not in {'signed', 'signing', 'reconciliation_required'}:
            raise ValueError('unsupported private signing operation status')
        if status != 'signed':
            if result_body:
                raise ValueError('uncertain private signing operation has a contradictory result')
            continue
        result = module.strict_json(result_body)
        extension = '.aab' if request['target'] == 'android-google-play' else '.apk'
        name = state_name + '/' + result['artifactDigest'] + extension
        if (not re.fullmatch(r'[a-f0-9]{64}', result['artifactDigest'])
                or result['artifactPath'] != str(original_root / name)
                or name not in records or records[name]['sha256'] != result['artifactDigest']
                or records[name]['size'] != result['artifactSize'] or result['operationKey'] != key
                or result['requestBinding'] != binding or result['certificateSHA256'] != request['expectedSigner']
                or result['inputArtifactId'] != request['artifactId'] or result['inputArtifactDigest'] != request['artifactDigest']):
            raise ValueError('signed object or receipt differs from the exact private journal')
    return rows


def backup(vault_root, config_path, key_file, crypto_tool, output):
    root = state.private_directory(Path(vault_root))
    state.private_directory(root.parent)
    config_path, output, key_file = Path(config_path), Path(output), Path(key_file)
    if key_file.resolve().is_relative_to(root.resolve()) or output.resolve().is_relative_to(root.resolve()):
        raise ValueError('recovery key and backup output must remain outside the private vault')
    if output.exists() or output.is_symlink():
        raise ValueError('private backup output is occupied')
    config_name = state.inside(root, config_path)
    config, state_name = configuration(root, config_path)
    state_root = root / state_name
    state.inside(root, state_root)
    state.private_directory(state_root)
    with state.maintenance(config, exclusive=True), tempfile.TemporaryDirectory(prefix='.signer-backup-', dir=root.parent) as scratch:
        scratch = Path(scratch)
        if (state_root / 'recovery-fence.json').exists():
            raise ValueError('keep the original recovery archive; a fenced restore cannot become a new active backup')
        paths = {config_name: config_path}
        for profile in config['profiles']:
            for field in SECRET_FIELDS:
                name = state.inside(root, Path(profile[field]))
                paths[name] = Path(profile[field])
        for path in state_root.iterdir():
            if path.name in {'.maintenance.lock', 'operations.sqlite', 'operations.sqlite-wal', 'operations.sqlite-shm'}:
                with state.private_stream(path, 256 << 20, allow_empty=True):
                    pass
                continue
            if not re.fullmatch(r'[a-f0-9]{64}\.(apk|aab)', path.name):
                raise ValueError('private state contains unknown or interrupted temporary entries')
            record = state.file_record(path)
            if record['sha256'] != path.stem:
                raise ValueError('private content-addressed signed object is corrupt')
            paths[state.inside(root, path)] = path
        snapshot = scratch / 'operations.sqlite'
        state.file_record(state_root / 'operations.sqlite')
        with closing(sqlite3.connect((state_root / 'operations.sqlite').as_uri() + '?mode=ro', uri=True)) as source:
            source.execute('PRAGMA query_only=ON')
            with closing(sqlite3.connect(snapshot)) as target:
                source.backup(target)
        snapshot.chmod(0o600)
        paths[state_name + '/operations.sqlite'] = snapshot
        records = {name: state.file_record(path) for name, path in paths.items()}
        if 'manifest.json' in records or len(records) > 10000 or sum(r['size'] for r in records.values()) > MAX_TOTAL:
            raise ValueError('private vault archive exceeds bounded scope')
        rows = validate_journal(config, root, root, state_name, records)
        if state.rows_digest(rows) != state.rows_digest(state.journal_rows(snapshot)):
            raise ValueError('private journal changed across the consistent snapshot')
        manifest = {'version': 1, 'originalRoot': str(root), 'configPath': config_name, 'statePath': state_name,
                    'createdAt': datetime.now(timezone.utc).isoformat(), 'files': records,
                    'journalRows': len(rows), 'journalDigest': state.rows_digest(rows)}
        plain = scratch / 'snapshot.tar'
        with plain.open('xb') as stream:
            os.fchmod(stream.fileno(), 0o600)
            with tarfile.open(fileobj=stream, mode='w', format=tarfile.USTAR_FORMAT) as archive:
                for name, path in sorted(paths.items()):
                    body = state.private_bytes(path, 256 << 20)
                    if hashlib.sha256(body).hexdigest() != records[name]['sha256']:
                        raise ValueError('private bytes changed during backup collection')
                    member = tarfile.TarInfo(name)
                    member.mode, member.size = 0o600, len(body)
                    archive.addfile(member, io.BytesIO(body))
                body = state.canonical(manifest).encode()
                if len(body) > 4 << 20:
                    raise ValueError('private manifest exceeds limit')
                member = tarfile.TarInfo('manifest.json')
                member.mode, member.size = 0o600, len(body)
                archive.addfile(member, io.BytesIO(body))
            stream.flush()
            os.fsync(stream.fileno())
        crypto('seal', crypto_tool, key_file, plain, output)
        return {'version': 1, 'action': 'backup', 'files': len(records), 'operations': len(rows),
                'archiveSHA256': state.file_record(output, MAX_ARCHIVE + 128)['sha256']}


def restore(destination, archive, key_file, crypto_tool):
    destination, archive = Path(destination), Path(archive)
    state.private_directory(destination.parent)
    if not destination.is_absolute() or destination.exists() or destination.is_symlink():
        raise ValueError('private recovery requires an absent absolute destination')
    with tempfile.TemporaryDirectory(prefix='.signer-restore-', dir=destination.parent) as scratch, \
            tempfile.TemporaryDirectory(prefix='.signer-vault-', dir=destination.parent) as staging:
        scratch = Path(scratch)
        plain = scratch / 'snapshot.tar'
        crypto('open', crypto_tool, key_file, archive, plain)
        stage = Path(staging)
        records, total = {}, 0
        manifest = None
        with tarfile.open(plain, mode='r:') as source:
            for member in source:
                name = state.safe_relative(member.name)
                if (not member.isfile() or member.mode != 0o600 or member.pax_headers or member.linkname
                        or not 0 < member.size <= 256 << 20 or name in records or len(records) >= 10001):
                    raise ValueError('unsafe, duplicate or oversized private archive entry')
                total += member.size
                if total > MAX_TOTAL + (4 << 20):
                    raise ValueError('private archive expansion exceeds limit')
                body = source.extractfile(member).read(member.size + 1)
                if len(body) != member.size:
                    raise ValueError('truncated private archive entry')
                if name == 'manifest.json':
                    if member.size > 4 << 20:
                        raise ValueError('private manifest exceeds limit')
                    manifest = state.strict_json(body)
                    records[name] = {'manifest': True}
                    continue
                path = stage / name
                path.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
                # mkdir(parents) uses the process umask for ancestors; make only our staging tree private.
                parent = path.parent
                while parent != stage:
                    parent.chmod(0o700)
                    parent = parent.parent
                private_write(path, body)
                records[name] = {'sha256': hashlib.sha256(body).hexdigest(), 'size': len(body), 'mode': 0o600}
        fields = {'version', 'originalRoot', 'configPath', 'statePath', 'createdAt', 'files', 'journalRows', 'journalDigest'}
        if not isinstance(manifest, dict) or set(manifest) != fields or type(manifest['version']) is not int or manifest['version'] != 1:
            raise ValueError('invalid authenticated private archive manifest')
        records.pop('manifest.json', None)
        if manifest['originalRoot'] != str(destination) or manifest['files'] != records:
            raise ValueError('private archive paths or bytes differ from the exact restore destination')
        config_name, state_name = state.safe_relative(manifest['configPath']), state.safe_relative(manifest['statePath'])
        if config_name not in records or state_name + '/operations.sqlite' not in records:
            raise ValueError('private archive omitted its configuration or journal')
        config, actual_state_name = configuration(destination, stage / config_name, actual_root=stage)
        if actual_state_name != state_name:
            raise ValueError('private configuration state path differs from manifest')
        rows = validate_journal(config, destination, stage, state_name, records)
        if manifest['journalRows'] != len(rows) or manifest['journalDigest'] != state.rows_digest(rows):
            raise ValueError('private journal differs from authenticated snapshot')
        expected_files = {config_name, state_name + '/operations.sqlite'}
        for profile in config['profiles']:
            expected_files.update(state.safe_relative(str(Path(profile[f]).relative_to(destination))) for f in SECRET_FIELDS)
        expected_files.update(name for name in records if re.fullmatch(re.escape(state_name) + r'/[a-f0-9]{64}\.(apk|aab)', name))
        if set(records) != expected_files:
            raise ValueError('private archive includes unexpected files')
        for name in records:
            if name.startswith(state_name + '/') and name.endswith(('.apk', '.aab')):
                if Path(name).stem != records[name]['sha256']:
                    raise ValueError('private content-addressed recovery object is corrupt')
        fence = {'version': 1, 'mode': 'successful-replay-only', 'originalRoot': str(destination),
                 'statePath': state_name, 'configDigest': hashlib.sha256(state.canonical(config).encode()).hexdigest(),
                 'journalDigest': state.rows_digest(rows), 'files': records}
        private_write(stage / state_name / 'recovery-fence.json', state.canonical(fence).encode())
        for parent in sorted((p for p in stage.rglob('*') if p.is_dir()), key=lambda p: len(p.parts), reverse=True):
            descriptor = os.open(parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try: os.fsync(descriptor)
            finally: os.close(descriptor)
        descriptor = os.open(stage, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try: os.fsync(descriptor)
        finally: os.close(descriptor)
        state.publish_directory(stage, destination)
        return {'version': 1, 'action': 'restore', 'files': len(records), 'operations': len(rows),
                'archiveSHA256': state.file_record(archive, MAX_ARCHIVE + 128)['sha256'],
                'recoveryMode': 'successful-replay-only'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='action', required=True)
    save = commands.add_parser('backup')
    for name in ['vault-root', 'config', 'key-file', 'crypto-tool', 'output']:
        save.add_argument('--' + name, type=Path, required=True)
    load = commands.add_parser('restore')
    for name in ['destination', 'archive', 'key-file', 'crypto-tool']:
        load.add_argument('--' + name, type=Path, required=True)
    args = vars(parser.parse_args())
    action = args.pop('action')
    if action == 'backup': args['config_path'] = args.pop('config')
    try:
        result = backup(**args) if action == 'backup' else restore(**args)
        print(state.canonical(result))
        return 0
    except (Exception, KeyboardInterrupt) as error:
        message = str(error) if isinstance(error, ValueError) else 'private signer recovery failed; inspect administrator-only state'
        print(message, file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
