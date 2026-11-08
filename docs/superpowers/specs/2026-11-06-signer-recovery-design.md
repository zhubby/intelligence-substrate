# Android Signer Backup and Recovery

This increment implements the approved platform's private signing-state recovery requirement. It does not complete P3 or provide an enterprise HSM, IAM or production disaster-recovery certification.

## Scope and boundaries

- An administrator CLI backs up one self-contained private signer vault: the administrator configuration, its referenced keystore/password files, a consistent SQLite journal and every journal-bound signed APK/AAB. SDKs, tools and repository code are not archived.
- All private inputs must reside beneath an explicit owned 0700 vault root, outside its signing state directory where required by the existing signer. Do not collect arbitrary filesystem paths. Existing separated `/etc`/`var` deployments must prepare an approved self-contained vault before using this tool; the backup command never migrates them.
- A separately generated 32-byte random recovery key encrypts a bounded archive with standard AES-256-GCM. Keys never appear in argv, environment, public reports or plaintext outputs. No password-based key derivation, custom streaming encryption or network service is introduced.
- Restore requires an absent destination at the same absolute vault path as the authenticated manifest, with an owned private parent. It never overwrites or merges an existing vault, changes profile bindings or accepts arbitrary extraction paths. Relocation is intentionally rejected because existing signing bindings contain administrator profile paths.
- Restore publishes a fully validated private tree and a recovery fence. It permits only inspection and successful known-operation replay. Unknown keys and started/failed operations cannot sign. An old backup is not proof that no later operation occurred; no CLI activation/reset bypass is provided. Reconciliation with the control plane before introducing a new active signer remains an enterprise operation.

## Consistency and private data

The signer and backup tool share a bounded, no-follow POSIX advisory maintenance lock for the full signing/snapshot operation. Backup takes the exclusive lock without waiting; a busy signer is a clear retryable administrative condition, before any new output. Inspection stays read-only and does not create state.

SQLite's backup API captures the journal, including unknown intents. Every successful row must have a private owned regular content-addressed artifact, a matching receipt/request binding, exact bytes/size and an allowed APK/AAB extension. Unreferenced normal objects are preserved; unsafe entries, active temporary directories, links and unsupported state entries reject backup. WAL/SHM and the maintenance lock are not restored as data files.

The authenticated tar is restricted to regular files with bounded names/count/size and a single strict manifest. It records exact SHA256/size/mode, original root/config/state paths and journal row count. Restore independently checks its exact file set, hashes, configuration reachability, SQLite integrity and every row/object relationship before publication. No `extractall` is used. Corrupt ciphertext, wrong key, unsafe archives, profile drift, missing objects and occupied targets leave no published vault.

The crypto CLI uses exclusive, durable private publication. Plaintext staging exists only inside administrator-owned 0700 temporary directories and is removed on normal exit. Forced process termination may leave private staging; administrators must inventory it before cleanup. Recovery keys and encrypted archives require separate custody. Recovery proves local temporary-key technical behavior, not enterprise custody/rotation or off-site retention.

## Acceptance

1. Actual AES-GCM encryption/decryption rejects wrong keys, tampering, truncation and unsafe file permissions without output leakage or overwrite.
2. Concurrent signing blocks backup; exclusive backup blocks signing before a new intent. Existing APK/AAB replay and unknown-intent rules remain intact.
3. Complete vault snapshot includes keys, consistent journal and exact signed objects; corruption, path/link tricks, omitted objects, schema drift and occupied destinations fail closed.
4. A real temporary AAB signing identity is backed up, its task-owned original vault is quarantined, the same path is restored, key possession and certificate are independently checked, and replay returns identical signed bytes/receipt without another signing invocation. Unknown and uncertain operations remain blocked.
5. Appropriate Go/Python regression, main-thread review, prior source/index/outputs/preview/Docker preservation, secret scanning, fresh deliverables and independent readback pass. No paid models, device/store/production actions, global Xcode changes, staging, commits, pushes or subagents.
