# App Store materials implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans sequentially in this session, as required by the user's AGENTS instructions. Steps use checkboxes for tracking.

**Goal:** Bind reviewed App Store text and screenshots to a release approval and manage them through the trusted publisher.

**Architecture:** An immutable report artifact supplies the release material digest. The existing publisher stages the administrator's matching private manifest, journals material writes, waits without a lease, and verifies App Asset Library placements before review submission.

**Tech Stack:** Go, SQLite/PostgreSQL, React/TypeScript, local TLS fixtures, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-06-appstore-materials-design.md`

## Global constraints

- Preserve index, unborn branch, 129 prior outputs and PID 36063/18080; no Git mutations or preview replacement.
- Prefix shell commands with `rtk`; run sequentially in the main thread.
- No real store writes, paid models, automatic Xcode acceptance or production approval.
- Use current App Asset Library API, runtime catalog and `uploaded: true`; never deprecated screenshot writes.
- Old operations without materials retain their existing request hash and read-only recovery semantics.

### Task 1: Immutable release material approval

**Files:** `internal/platform/release_materials.go`, `internal/platform/release_materials_test.go`, `internal/platform/releases.go`, `dispatch.go`, `channels.go`, `internal/worker/runner.go`, `publisher_test.go`, `internal/publisher/publisher.go`, `web/src/forms.tsx`, `web/src/pages/Operations.tsx`, `web/src/styles.css`, `web/tests/release-materials.spec.ts`.

**Interfaces:** `storeMetadataArtifactIds: string[]` in `releases.create`; release `storeMetadata` entries `{id,target,digest}`; task and publisher request `metadataDigest` (omitted when absent).

- [x] Write and run failing tests for different material digests changing approval binding, wrong goal/revision/commit/repository/target/type rejection, producer self-approval, content tampering and propagation through all three tasks and the actual worker subprocess.
- [x] Add scoped artifact selection/verification to candidate creation and gate; include the material entries only when present in the approval hash to preserve old candidates.
- [x] Add report picker/downloadable review summary and approval confirmation; browser-test real candidate creation, selection, download, digest display and approval.

### Task 2: Private manifest and runtime catalog

**Files:** `internal/publisher/apple_material.go`, `apple_catalog.go`, `apple_material_test.go`, `deploy/appstore-metadata.example.json`, `publisher.appstore.example.json`.

**Interfaces:** `stageAppleMaterial(Profile, Request) (*appleMaterial, error)` stages byte-exact images and a bounded complete text manifest; snapshot has no local paths. Runtime catalog accepts only the requested feature/group/spec/file combinations.

- [x] Write/run failing tests against actual private JSON/image files for mismatched request/profile/file SHA256, duplicate JSON/locale/group, bad URL/text, symlink, malformed or oversized/transparent image, and changed image bytes.
- [x] Implement optional managed configuration and strict manifest decoding, version/app binding, bounded text/URL checks and private PNG/JPEG staging.
- [x] Write/run catalog tests with hand-authored feature/policy/spec fixtures, including unavailable feature, invalid platform, excessive count, incompatible dimensions/MIME/alpha/spec and ambiguous entries; implement current documented field names.

### Task 3: Durable material upload and independent reads

**Files:** `internal/publisher/apple_material_api.go`, `apple_material_upload.go`, `apple_material_journal.go`, `apple_material_execution_test.go`, `apple_material_recovery_test.go`, `apple.go`, `apple_package.go`, `apple_recovery.go`, `publisher.go`.

**Interfaces:** Material snapshots in `appleReceipt`; app and version/build reservations in the publisher SQLite state. Existing publish/status/continue remain the public interface.

- [x] Write/run failing local TLS tests for exact metadata PATCH, App Asset Library reserve/byte PUT/commit without JWT on blobs or deprecated checksum, processing wait and ordered placement submission.
- [x] Implement parent-scoped complete localization/media discovery, runtime library/catalog validation, durable intent checks, atomic reservation, text updates and image uploads before existing IPA upload.
- [x] Return processing without polling. Read-only status checks exact images/build and emits continuation only when ready.
- [x] Write/run failure tests for drift before linking and final submission, unknown responses at each material write, journal state/hash/lock change, different key/reopen/concurrency and stale/paginated/extra media. Implement guarded placement/order and final verification.
- [x] Extend accepted-operation read-only recovery tests and managed snapshot/CAS/lock handling. No auto-recovery of uncertain material writes.

### Task 4: Verification and reviewable delivery

**Files:** `README.md`, `docs/api.md`, `docs/app-store.md`, `docs/releases.md`, `docs/workers.md`, `docs/validation.md`, `.context/compound-engineering/todos/004-ready-p1-appstore-materials.md`; verification scripts/evidence only in ignored `work/`.

- [x] Review correctness, API contract, security and simplicity in the main thread; fix actionable findings using a failing regression test.
- [x] Run scoped tests once per change, then full vet/race on a dedicated PostgreSQL cluster, Web build/affected browser tests, 21 cross-compilations, formatting/tidy/diff checks. Stop the task cluster/test service.
- [x] Probe Apple prerequisites, record license result without accepting it, and distinguish local protocol from live Apple/device acceptance.
- [x] Export a new source ZIP, Chinese report/runbook and machine-readable evidence to the established outputs directory; independently read back source/log digests, prior outputs, unchanged index and live preview.
- [x] Complete only this todo; keep the overall platform goal and native/store/device acceptance open. No commit or push.

## Verified completion of this increment

- Final full vet/race: 297 top-level / 1020 including subtests; 12 packages without tests, no skipped tests or failures. Dedicated PostgreSQL 17.6 loopback 53688 stopped.
- 21 Go builds; six real browser scenarios and two final layout rechecks; five actual CLI refusals, zero reservations. 53 official Apple documents indexed.
- First independent readback passed for all 331 source files and six new outputs. All 129 prior output digests and 261 staged entries preserved. The index-before.bin snapshot is git ls-files output, not raw index bytes.
- Original preview backend PID 36063 remained running; static UI rebuilt from previous source after an accidental build overwrite, with no pre-overwrite byte identity claim. Final new UI used isolated 18082, now stopped.
- Both Apple SDK probes remain 69. Overall platform/P3, real Apple signing/account/device acceptance and native builds remain open. No commit/push or production action.
