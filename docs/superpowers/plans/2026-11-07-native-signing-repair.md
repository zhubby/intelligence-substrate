# Native Signing Repair Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in the main thread. User instructions prohibit delegation and Git index/commit changes.

**Goal:** Continue failed independent QA of signed Apple/Windows packages through governed source repair, a fresh unsigned build and signature, then exact final-package verification.

**Architecture:** Generalize the existing bounded signing repair state machine without migrating Android records. Exclude macOS execution observations from immutable recipes while keeping their separate signing intent checks; clear them only on new derived tasks.

**Tech Stack:** Go, SQLite/PostgreSQL, existing Worker/HTTP/Git and React/Playwright.

**Spec:** `docs/superpowers/specs/2026-11-07-native-signing-repair-design.md`

## Global Constraints

- Main thread, existing feature branch; no index changes, commits, pushes, paid models, production or store actions.
- Baseline: 755 sources, 339 historical outputs, zero commits, 261 index records. Evidence: `work/signing-repair-p_glmy7h`; preserve original preview, Docker, AVD and keychain configuration.
- Maximum two repair rounds. New task and operation key each time; unchanged approved identity and quality policy. No uncertain signing retries.
- Do not accept Apple license. Synthetic signatures and protocol fixtures do not establish native or physical-device acceptance. Overall P3 remains open.

### Task 1: Governed continuation for all signing modes

**Files:** Modify `internal/platform/android_signing_repair.go`, its call sites and native tests; create `internal/platform/native_signing_repair_test.go`.

**Interfaces:** `startSigningRepair`, `validateSigningRepair`, `afterSigningRepair` retain existing Resource/Tx contracts. `signingRepairRecord` reads version 1 Android or version 2 native records, rejects mixed stores and validates identity. Other callers validate the same authority before claim, heartbeat, source/artifact upload, completion and evidence use.

- [x] Add native fixture tests: approved code → unsigned → sign (macOS with real persisted wait/status state) → failed protected QA → code repair; assert new unsigned/sign/recheck rather than blocked generic rebuild. Run and retain expected red output.
- [x] Generalize state machine and all call sites. Keep Android version 1 digests valid. Add version 2 identity and record validation; exclude/reset macOS observation fields on fresh continuation only.
- [x] Test two rounds from latest failure, preserved identities, changed records/recipes/policy/source, source recovery, revoked authority, uncertain signing, and release gates. Re-run Android repair tests unchanged.

### Task 2: Execution and persistence proof

**Files:** Create `internal/platform/native_signing_repair_postgres_test.go`, `internal/worker/native_signing_repair_test.go`.

- [x] Exercise independent PostgreSQL connections with two signers claiming the same fresh repair, replay completion, backup/restore exact resources and audit integrity, and prove restore does not repeat completed work.
- [x] On macOS exercise actual Worker/HTTP/Git/subprocess for iOS/macOS with deterministic broken source then repair and fixed signer fixture. Assert independently checked final bytes, fresh operations, no source access by signer, no unsigned QA, and no physical/production release shortcut.
- [x] Preserve raw resource snapshots, artifacts, backups and audit for independent readback; clearly label OS/signature substitutes.

### Task 3: Product surface and delivery

**Files:** Modify `web/src/pages/Engineering.tsx`; create native signing repair browser scenarios, `docs/native-signing-repair.md`; update related documentation, validation and the main implementation ledger. Align full race timeout in `Makefile`, `.github/workflows/ci.yml` and `README.md` with the verified 20-minute per-package runner setting.

- [x] Add browser assertions for repairing, waiting for new signature, macOS notarization wait, recheck, closed and blocked states at desktop/mobile widths; first observe absent repair detail red.
- [x] Add defect repair detail from authoritative task/record state using the existing UI visual language. Inspect final screenshots.
- [x] Run complete Go race with isolated PostgreSQL, vet/build/tidy-diff, Python regression, TypeScript/build/format and focused browser regression. Review state/permission/compatibility code sequentially.
- [x] Stop only owned test processes. Verify original outputs, index, preview, Docker/AVD/keychain against baseline. Export a Chinese report, runbook, source ZIP and evidence under a new outputs prefix; independently read back every exported source hash/mode and core proof records.
- [x] Close only todo041, re-export completion documentation and independently read back. Keep the full-product goal active and state native/production gaps.

## Completion evidence

Only increment041 local implementation/verification is complete. Full product/P3 remains open. Initial four deliverables and every source byte/size/mode were independently read back; completion-only documentation is included in the final re-export and readback. Raw evidence: work/signing-repair-p_glmy7h. No paid model, real native signature/device/store/production, staging, commit or push action. Apple SDK still license69.
