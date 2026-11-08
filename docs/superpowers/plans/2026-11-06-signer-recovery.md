# Android Signer Recovery Implementation Plan

> **For agentic workers:** Execute sequentially with superpowers:executing-plans. User instructions prohibit subagents and staging/commit/push.

**Goal:** Recover an encrypted, consistent private Android signing vault while preserving exact successful replay and blocking unsafe work from stale snapshots.

**Architecture:** A small standard-library Go crypto CLI seals/opens a bounded private archive. Python owns vault/config/SQLite validation and shares a POSIX maintenance lock plus restored-state fence with the fixed signer. Restore preserves exact absolute profile paths.

**Tech Stack:** Existing Go 1.27.1, AES-256-GCM, Python 3.10+, SQLite backup API, POSIX flock, existing real Java/bundletool tools.

**Spec:** `docs/superpowers/specs/2026-11-06-signer-recovery-design.md`.

## Global constraints

Preserve the 484-source/229-output/261-index/zero-commit baseline under `work/signer-recovery-gklzhred`. No original preview or Docker changes. All fixtures, credentials and archives remain in private ignored work directories; public deliverables contain no private bytes or encrypted private backup. Recovery supports only the original absolute vault root and defaults to successful-replay-only; no activation/reset API.

### Task 1: Authenticated private archive

**Files:** `internal/signingarchive/archive.go`, `archive_test.go`, POSIX/Windows private-file helpers; `cmd/signer-archive/main.go` and tests.

**Interfaces:** `keygen --output PATH`, `seal --key-file PATH --input PATH --output PATH`, `open` with the same arguments. Raw key is 32 bytes; encrypted format has a fixed versioned header, random GCM nonce and authenticated payload. Private regular inputs and owned private output parents are required; all publication is exclusive and fsynced. Maximum plaintext is 512 MiB.

- [x] Write behavior tests for actual roundtrip, fresh nonces, wrong key/header/ciphertext/truncation, bounded input, private-file and overwrite refusal. Observe the missing command fail first.
- [x] Implement the crypto format and private durable publication without external dependencies. CLI reports only generic status, never secret values.
- [x] Run Go crypto/CLI tests and vet; verify non-POSIX builds explicitly reject private vault operations. The Windows refusal path was reviewed and cross-compiled, not executed on Windows.

### Task 2: Snapshot, restore and signer fence

**Files:** `scripts/android_signer_state.py`, `scripts/signer-backup.py`, `scripts/test_signer_backup.py`; modify `scripts/sign-android-apk.py` and signing tests.

**Interfaces:** `backup --vault-root ROOT --config CONFIG --key-file KEY --crypto-tool TOOL --output ARCHIVE`; `restore --destination ROOT --archive ARCHIVE --key-file KEY --crypto-tool TOOL`. Shared `maintenance(config, exclusive=False)` guards execution. A restored `recovery-fence.json` fixes config and journal hashes; existing successful replay is accepted, missing/non-successful keys rejected before SDK execution. Inspect does not write.

- [x] Write subprocess/API behavior tests that first fail for the absent manager/fence, including actual SQLite/object consistency, archive traversal/duplicates, missing/tampered artifacts, unsafe paths/permissions, stale/unknown intents and lock contention.
- [x] Implement strict manifest and vault collection, read-only SQLite snapshot, encrypted exclusive backup and all-before-publication restore. Preserve request/profile/result paths and every durable row.
- [x] Wrap signer execution in the shared lock and enforce the immutable restored fence before tools or intent creation. Run all Python tests and verify existing APK/AAB behaviors.

### Task 3: Actual recovery, delivery and final review

**Files:** work-only real acceptance/export/readback scripts; `docs/signer-recovery.md`, signing/AAB/validation/README references; canonical todo019.

**Interfaces:** Fresh temporary key/cert/profile uses the prior exact unsigned AAB read-only. Original task-owned vault is quarantined; recovery publishes the same original path. Independent certificate/CSR and Java bundle checks verify identity and unchanged signed bytes. No original signing state or keystore is mutated.

- [x] Exercise actual keygen, AAB signing, consistent backup, wrong-key/corrupt/occupied-path failures, quarantined-vault restore, private-key proof, exact replay and unknown/uncertain refusal. Save public digests and private logs separately.
- [x] Run appropriate regression/vet/build, review crypto/permissions/atomicity/recovery fences sequentially, and verify source/index/prior outputs/preview/Docker preservation. Leave broad P3 open.
- [x] Export source/report/runbook/evidence under new output names, scan private bytes and independently read back source hashes, crypto/SQLite/artifact evidence and report links. Complete only todo019 after the evidence passes.

## Evidence checkpoint — 2026-11-06

Evidence root: `work/signer-recovery-gklzhred`.

- `final-checks-result.json`: 106 Python tests; archive/CLI Go race 4 top-level / 19 with subtests, no failures/skips; all-package vet/build, formatting and tidy-diff. Linux/Windows binaries and the Windows unconditional-refusal test cross-compile. No Linux/Windows native recovery or repeated full-platform/browser/remote-CI claim.
- `actual-final/actual-recovery-result.json`: actual new temporary RSA identity, same-path encrypted vault recovery, identical two-row journal/receipt/AAB and only one jarsigner call. Restored certificate SHA256 `3ebea83ead2511f6bd155ea06eabace66570831edc37040f46099c0e75a77633`; OpenSSL verifies CSR signature and matching certificate public key, while Java verifies 77 signed payload entries. Original vaults and first-round history remain intact.
- `main-thread-review.json`, `alias-red.stderr` and `alias-green.stderr`: sequential security/simplicity review and concrete resolved-parent custody fix. No subagents or independent code reviewer.
- `preservation-result.json`: 498 current source files, 229 prior outputs, 261 index entries, zero commits, original preview/static bytes and AVD retained. The 37 stable containers, 23 image references and 16 volumes remain; an unrelated captured CyberOS build container ended and is explicitly recorded as missing rather than silently replaced. Task issued no Docker mutation.
- Initial `export-result.json` and `independent-readback-result.json` pass for four secret-scanned deliverables. Independent private-archive decryption checks all five data files, exact journal rows, source hashes, original/restored signing receipts/AAB, CSR/public-key proof, links and previous outputs. Completion metadata is followed by a refreshed export/readback.

Only this recovery increment is complete. The archive preserves existing profile paths and supports successful known replay; automatic activation, rotation/migration, enterprise custody/HSM/off-site DR, target-platform native acceptance and P3 remain open. Both actual Apple SDK checks still exit 69; no license acceptance, global SDK switch, staging/commit/push, paid model, store or production action occurred.
