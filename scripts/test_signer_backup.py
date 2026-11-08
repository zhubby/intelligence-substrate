"""Vault recovery behavior; fixture APKs do not prove native signature validity."""
from concurrent.futures import ThreadPoolExecutor
from contextlib import closing
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import time
import unittest
import test_sign_android_apk as fixture


class SignerBackupTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tool_dir = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.tool_dir.cleanup)
        cls.crypto = Path(cls.tool_dir.name) / 'signer-archive'
        supplied = os.environ.get('IS_SIGNER_ARCHIVE_TEST_BINARY')
        if supplied:
            shutil.copyfile(supplied, cls.crypto)
            cls.crypto.chmod(0o700)
        else:
            subprocess.run(['go', 'build', '-o', str(cls.crypto), './cmd/signer-archive'],
                           cwd=Path(__file__).resolve().parents[1], check=True, capture_output=True)

    def setUp(self):
        manager = Path(__file__).with_name('signer-backup.py')
        self.assertTrue(manager.is_file(), 'private signing-vault recovery is not implemented')
        spec = importlib.util.spec_from_file_location('signer_backup', manager)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.fx = fixture.TrustedAndroidSigningTests(methodName='runTest')
        self.fx.setUp()
        self.addCleanup(self.fx.doCleanups)
        self.parent = self.fx.root
        self.vault = self.parent / 'vault'
        self.vault.mkdir(mode=0o700)
        for attribute in ['state', 'keystore', 'password', 'config_path']:
            old = getattr(self.fx, attribute)
            new = self.vault / old.name
            old.rename(new)
            setattr(self.fx, attribute, new)
        self.fx.profile.update(keystoreFile=str(self.fx.keystore), storePasswordFile=str(self.fx.password),
                               keyPasswordFile=str(self.fx.password))
        self.fx.write_config()
        self.original = self.fx.execute()
        self.key = self.parent / 'recovery.key'
        subprocess.run([str(self.crypto), 'keygen', '--output', str(self.key)], check=True, capture_output=True)
        self.encrypted = self.parent / 'vault.enc'

    def backup(self, **changes):
        args = dict(vault_root=self.vault, config_path=self.fx.config_path, key_file=self.key,
                    crypto_tool=self.crypto, output=self.encrypted)
        args.update(changes)
        return self.module.backup(**args)

    def restore(self, **changes):
        args = dict(destination=self.vault, archive=self.encrypted, key_file=self.key, crypto_tool=self.crypto)
        args.update(changes)
        return self.module.restore(**args)

    def quarantine(self):
        self.vault.rename(self.parent / 'quarantined-vault')

    def test_exact_vault_restore_replays_success_and_blocks_new_or_uncertain_work(self):
        request = {k: v for k, v in self.fx.request.items() if k != 'artifactPath'}
        request.update(operationKey='sign-uncertain', runId='run_uncertain')
        binding = hashlib.sha256(self.fx.module.canonical({'request': request, 'profile': self.fx.profile}).encode()).hexdigest()
        with closing(self.fx.module.database(self.fx.config())) as db:
            db.execute('INSERT INTO operations(key,binding,status,request,updated) VALUES(?,?,?,?,?)',
                       ('sign-uncertain', binding, 'signing', self.fx.module.canonical(request), 'uncertain'))
        original_config = self.fx.config_path.read_bytes()
        secret = self.fx.keystore.read_bytes()
        result = self.backup()
        self.assertEqual(result['operations'], 2)
        self.assertNotIn(secret, self.encrypted.read_bytes())
        self.quarantine()
        restored = self.restore()
        self.assertEqual(restored['recoveryMode'], 'successful-replay-only')
        self.assertEqual(self.fx.config_path.read_bytes(), original_config)
        self.assertEqual(self.fx.keystore.read_bytes(), secret)
        self.assertEqual(self.fx.execute(), self.original)
        self.assertEqual(len((self.fx.sdk / 'sign-calls').read_text().splitlines()), 1)
        for changes in [dict(operationKey='sign-new'), dict(operationKey='sign-uncertain', runId='run_uncertain')]:
            with self.subTest(changes=changes), self.assertRaisesRegex(ValueError, 'recovery|reconciliation'):
                self.fx.execute(**changes)
        self.assertEqual(self.fx.module.inspect(self.fx.config(), 'sign-uncertain')['status'], 'signing')
        self.assertEqual(len((self.fx.sdk / 'sign-calls').read_text().splitlines()), 1)

    def test_backup_and_restore_never_overwrite_or_relocate(self):
        self.backup()
        before = self.encrypted.read_bytes()
        with self.assertRaises(ValueError):
            self.backup()
        self.assertEqual(self.encrypted.read_bytes(), before)
        with self.assertRaises(ValueError):
            self.restore()
        with self.assertRaises(ValueError):
            self.restore(destination=self.parent / 'different-root')
        self.assertFalse((self.parent / 'different-root').exists())
        self.assertEqual(self.fx.execute(), self.original)

    def test_wrong_key_and_modified_ciphertext_publish_no_vault(self):
        self.backup()
        wrong = self.parent / 'wrong.key'
        subprocess.run([str(self.crypto), 'keygen', '--output', str(wrong)], check=True, capture_output=True)
        self.quarantine()
        with self.assertRaises(ValueError):
            self.restore(key_file=wrong)
        self.assertFalse(self.vault.exists())
        body = bytearray(self.encrypted.read_bytes())
        body[-1] ^= 1
        self.encrypted.write_bytes(body)
        with self.assertRaises(ValueError):
            self.restore()
        self.assertFalse(self.vault.exists())

    def test_missing_corrupt_linked_and_unexpected_state_files_reject_backup(self):
        signed = Path(self.original['artifactPath'])
        saved = signed.read_bytes()
        for mode in ['missing', 'corrupt', 'linked', 'temporary', 'unknown-entry']:
            with self.subTest(mode=mode):
                extra = None
                if mode == 'missing': signed.unlink()
                elif mode == 'corrupt': signed.write_bytes(b'corrupt signed object')
                elif mode == 'linked':
                    signed.unlink()
                    signed.symlink_to(self.fx.keystore)
                elif mode == 'temporary':
                    extra = self.fx.state / '.signing-interrupted'
                    extra.mkdir()
                else:
                    extra = self.fx.state / 'unrecognized-data'
                    extra.write_bytes(b'unrecognized')
                    extra.chmod(0o600)
                with self.assertRaises(ValueError):
                    self.backup()
                self.assertFalse(self.encrypted.exists())
                if extra:
                    if extra.is_dir(): extra.rmdir()
                    else: extra.unlink()
                if signed.exists() or signed.is_symlink(): signed.unlink()
                signed.write_bytes(saved)
                signed.chmod(0o600)

    def test_private_paths_and_permissions_cannot_escape_vault(self):
        old = self.fx.profile['keystoreFile']
        outside = self.parent / 'outside.p12'
        outside.write_bytes(self.fx.keystore.read_bytes())
        outside.chmod(0o600)
        self.fx.profile['keystoreFile'] = str(outside)
        self.fx.write_config()
        with self.assertRaises(ValueError): self.backup()
        self.fx.profile['keystoreFile'] = old
        self.fx.write_config()
        self.fx.password.chmod(0o644)
        with self.assertRaises(ValueError): self.backup()
        self.assertFalse(self.encrypted.exists())

    def test_busy_signing_blocks_snapshot_and_backup_lock_blocks_new_intents(self):
        state = self.module.state
        with state.maintenance(self.fx.config(), exclusive=True):
            with self.assertRaisesRegex(ValueError, 'maintenance|busy'):
                self.fx.execute(operationKey='sign-blocked')
        pause = self.fx.sdk / 'pause-sign'
        entered = self.fx.sdk / 'sign-entered'
        entered.unlink()
        pause.touch()
        try:
            with ThreadPoolExecutor(max_workers=1) as pool:
                future = pool.submit(self.fx.execute, operationKey='sign-paused', runId='run_paused')
                deadline = time.monotonic() + 5
                while not entered.exists() and time.monotonic() < deadline: time.sleep(.02)
                self.assertTrue(entered.exists(), 'fixture signing process did not reach held operation')
                with self.assertRaisesRegex(ValueError, 'maintenance|busy'):
                    self.backup()
                pause.unlink()
                future.result(timeout=10)
        finally:
            pause.unlink(missing_ok=True)
        with self.assertRaises(ValueError):
            self.fx.module.inspect(self.fx.config(), 'sign-blocked')
        self.assertFalse(self.encrypted.exists())

    def test_recovery_fence_detects_changed_config_objects_and_journal(self):
        self.backup()
        self.quarantine()
        self.restore()
        original = self.fx.password.read_bytes()
        self.fx.password.write_bytes(b'different-private-password\n')
        with self.assertRaisesRegex(ValueError, 'recovery'):
            self.fx.execute()
        self.fx.password.write_bytes(original)
        with closing(self.fx.module.database(self.fx.config())) as db:
            db.execute("UPDATE operations SET updated='changed after snapshot' WHERE key='sign-test'")
        with self.assertRaisesRegex(ValueError, 'recovery'):
            self.fx.execute()

    def test_authenticated_but_unsafe_archives_are_rejected_before_publication(self):
        self.backup()
        plain = self.parent / 'private.tar'
        subprocess.run([str(self.crypto), 'open', '--key-file', str(self.key), '--input', str(self.encrypted),
                        '--output', str(plain)], check=True, capture_output=True)
        with tarfile.open(plain) as archive:
            entries = [(member.name, archive.extractfile(member).read()) for member in archive.getmembers()]
        self.quarantine()
        for mode in ['traversal', 'duplicate', 'symlink', 'omitted-object', 'wrong-hash']:
            with self.subTest(mode=mode):
                altered = self.parent / (mode + '.tar')
                with tarfile.open(altered, 'w', format=tarfile.USTAR_FORMAT) as archive:
                    for name, body in entries:
                        if mode == 'omitted-object' and name.endswith('.apk'): continue
                        if mode == 'wrong-hash' and name.endswith('.apk'): body += b'altered'
                        member = tarfile.TarInfo(name)
                        member.mode, member.size = 0o600, len(body)
                        archive.addfile(member, io.BytesIO(body))
                    if mode in {'traversal', 'duplicate', 'symlink'}:
                        name = '../escaped' if mode == 'traversal' else entries[0][0] if mode == 'duplicate' else 'linked'
                        member = tarfile.TarInfo(name)
                        member.mode = 0o600
                        if mode == 'symlink': member.type, member.linkname = tarfile.SYMTYPE, '/private/elsewhere'
                        archive.addfile(member, io.BytesIO(b''))
                altered.chmod(0o600)
                encrypted = altered.with_suffix('.enc')
                subprocess.run([str(self.crypto), 'seal', '--key-file', str(self.key), '--input', str(altered),
                                '--output', str(encrypted)], check=True, capture_output=True)
                with self.assertRaises(ValueError): self.restore(archive=encrypted)
                self.assertFalse(self.vault.exists())
                self.assertFalse((self.parent / 'escaped').exists())

    def test_exclusive_publication_preserves_a_concurrently_occupied_directory(self):
        staging, occupied = self.parent / 'private-stage', self.parent / 'competing-destination'
        staging.mkdir(mode=0o700)
        (staging / 'data').write_bytes(b'private staging content')
        occupied.mkdir(mode=0o700)
        before = occupied.stat()
        with self.assertRaises(ValueError):
            self.module.state.publish_directory(staging, occupied)
        self.assertEqual(occupied.stat().st_ino, before.st_ino)
        self.assertEqual(list(occupied.iterdir()), [])
        self.assertEqual((staging / 'data').read_bytes(), b'private staging content')

    def test_stale_snapshot_cannot_repeat_a_signature_completed_after_backup(self):
        self.backup()
        self.fx.execute(operationKey='sign-after-backup', runId='run_after_backup')
        self.assertEqual(len((self.fx.sdk / 'sign-calls').read_text().splitlines()), 2)
        self.quarantine()
        self.restore()
        with self.assertRaisesRegex(ValueError, 'recovery'):
            self.fx.execute(operationKey='sign-after-backup', runId='run_after_backup')
        self.assertEqual(len((self.fx.sdk / 'sign-calls').read_text().splitlines()), 2)
        self.assertEqual(self.fx.execute(), self.original)

    def test_schema_and_profile_binding_drift_prevent_incomplete_backups(self):
        with closing(self.fx.module.database(self.fx.config())) as db:
            db.execute('CREATE TABLE unexpected(value TEXT)')
        with self.assertRaisesRegex(ValueError, 'schema'):
            self.backup()
        with closing(self.fx.module.database(self.fx.config())) as db:
            db.execute('DROP TABLE unexpected')
        self.fx.profile['javaHome'] = '/private/changed-identity-tools'
        self.fx.write_config()
        with self.assertRaisesRegex(ValueError, 'binding'):
            self.backup()
        self.assertFalse(self.encrypted.exists())

    def test_recovery_key_and_output_cannot_enter_vault_through_parent_aliases(self):
        alias = self.parent / 'vault-alias'
        alias.symlink_to(self.vault, target_is_directory=True)
        internal_key = self.vault / 'recovery.key'
        internal_key.write_bytes(self.key.read_bytes())
        internal_key.chmod(0o600)
        (self.vault / 'nested').mkdir(mode=0o700)
        for changes in [dict(key_file=alias / 'recovery.key'), dict(output=alias / 'nested/backup.enc')]:
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.backup(**changes)
        self.assertFalse(self.encrypted.exists())
        self.assertFalse((self.vault / 'nested/backup.enc').exists())


if __name__ == '__main__':
    unittest.main()
