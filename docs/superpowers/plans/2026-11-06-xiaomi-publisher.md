# Xiaomi Publisher Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in this thread. AGENTS.md prohibits subagents and this task does not authorize commit/push or staging changes.

**Goal:** Add the Xiaomi new-application/update submission path with approved material binding, failure-safe writes and truthful manual state reconciliation.

**Architecture:** Fixed administrator profile and private material manifest; reuse actual APK verification, then stream verified staged multipart data through a fixed HTTPS client. Persist package reservation/write intent before external mutation, retain uncertain operations, and never convert latest-package data into store publication.

**Tech Stack:** Go standard library multipart/RSA/MD5/SHA256, existing SQLite publisher journal, Android SDK, current platform/manual release API.

**Spec:** `../specs/2026-11-06-xiaomi-publisher-design.md`.

## Global Constraints

- Main-thread sequential execution; all local shell/verification entrypoints use RTK.
- Preserve 117 output SHA256s, original preview, credentials, SDKs, databases and source staging; no commit/push, paid models or real store actions.
- Only fixed `android-xiaomi`/`xiaomi-market`, verified release APK and exact reviewed material; no debug signer, mutable unchecked attachments, arbitrary API host or credential propagation.
- Official API has no sandbox or audit/on-shelf query. Push acceptance, actual publication and independent device validation remain separate; platform human reconciliation governs actual store state.

### Task 1: Approved material and SDK boundary

**Files:** `internal/publisher/xiaomi_material.go`, `internal/publisher/xiaomi_test.go`, `internal/publisher/publisher.go`.

**Interfaces:** Profile adds optional metadataFile/metadataDigest; metadata binds structured new/update app fields, icon and screenshot descriptors; package verification uses the existing SDK verifier.

- [x] Write and observe failing profile/material tests: unsupported driver, missing/changed metadata, initial screenshot requirements, invalid privacy URL, attachment tampering and unrelated destination selectors.
- [x] Implement strict profile/material decoding, exact SHA256/size staging and bounded image checks; keep existing driver request hashes stable for omitted new fields.

### Task 2: Official signed multipart and durable lifecycle

**Files:** `internal/publisher/xiaomi_protocol.go`, `internal/publisher/xiaomi.go`, `internal/publisher/xiaomi_test.go`, `internal/publisher/publisher.go`.

**Interfaces:** Execute delegates to Xiaomi; credentials contain userName/accessPassword/publicCertificatePEM. Fixed POST multipart query/push; Receipt.Xiaomi persists version/intent, never SIG or access password.

- [x] Write failing TLS tests that decrypt SIG independently, verify all file MD5s and actual uploaded bytes, initial/update permissions and no credentials/cookies/redirect leakage.
- [x] Implement fixed HTTPS protocol, bounded complete response parsing and exclusive persisted package/write reservation; confirmed push returns awaiting_review and atomically releases local reservation.
- [x] Verify lost response/API error retains intent and never retries, concurrent/reopened engines do not duplicate writes, original result is idempotent, changed binding/material is denied, and status never returns published.

### Task 3: Platform, real CLI and delivery

**Files:** Xiaomi deploy examples, `docs/xiaomi-market.md`, README/releases/workers/validation documentation; platform target acceptance test if current generic paths need proof.

- [x] Verify Xiaomi's manual portal state and independent acceptance gates through the existing platform API; preserve no unsupported status-automation promise.
- [x] Run actual SDK/CLI rejection cases using the existing diagnostic APK, plus appropriate full Go vet/race/PostgreSQL and three-platform binary compilation. Preserve original services and artifacts; stop only task-owned test services.
- [x] Review correctness/permissions/retry boundaries in the main thread. Export new source/report/runbook/evidence, independently read bytes and verify all 117 original digests. Keep overall goal active and real account/native/P3 gaps explicit.

## Completion evidence

Final full Go vet/race: 250 top-level tests, 646 including subtests, zero failures or skipped tests; 14 Xiaomi top-level tests / 56 including subtests are included in that total. Dedicated PostgreSQL 17.6 stopped; 21 platform program compilations; four actual SDK/CLI rejection cases. Evidence: work/xiaomi-publisher-12h23fte/{regression/regression-result.json,cli/cli-evidence.json,review-and-preservation.json}. Export and byte readback are verified by work/export-xiaomi.py and the independent closure check. Original 117 output digests, preview PID 36063 and staging are preserved. Apple SDK prerequisites still exit 69. No real account, signed-success, store publication or device acceptance; overall product/P3 remains incomplete.
