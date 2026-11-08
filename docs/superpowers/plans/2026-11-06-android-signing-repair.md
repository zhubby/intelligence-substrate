# Android Signing Repair Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan sequentially in the main thread. User instructions prohibit subagents and commits.

**Goal:** Repair independently rejected signed Android packages using approved recipes and fresh signing operations, then reverify exact final bytes.

**Architecture:** Extend the existing defect state machine with durable per-round authority and continuation records. Keep unsigned build, signing and protected independent QA separate; validate authority wherever work or evidence can be accepted.

**Tech Stack:** Go control plane/Worker, SQLite/PostgreSQL, Python/Flutter/Android SDK, existing local emulator.

**Spec:** `docs/superpowers/specs/2026-11-06-android-signing-repair-design.md`

## Global Constraints

- Main thread only; no staging, commits, pushes, production, stores or paid model calls.
- Keep original preview 18080/PID36063, Docker 37/23/15, 21 original AVD files, 261 Git index records, zero commits and 209 existing output hashes intact.
- Baseline is 463 platform source files. Use `work/android-signing-repair-smydl_pp` for private evidence; publish deliverables only in the established outputs directory.
- No acceptance of Apple agreements or global developer selection changes. Test certificates are temporary local identities. Emulator checks are not physical or online business acceptance.
- Maximum two repair rounds per defect; new signing task and operationKey every round; never clear or retry a previous signing intent.

### Task 1: Governed repair state machine

**Files:** Create `internal/platform/android_signing_repair.go` and `_test.go`; modify `defects.go`, `tasks.go`, `goals.go`, `android_signing.go`.

**Interfaces:** `startAndroidSigningRepair` records authority when a code repair is created; `afterAndroidSigningRepair` creates the new chain after trusted code completion; `validateAndroidSigningRepair` enforces records before claim/upload/completion/artifact use.

- [x] Add a failing real-service test that includes approved code→unsigned→sign, fails bound QA and completes code repair; assert a new unsigned→sign→test chain instead of a blocked defect.
- [x] Run `rtk proxy go test ./internal/platform -run AndroidSigningRepair -count=1 -v`; retain the expected red log.
- [x] Implement durable authority/continuation records, strict derivation from the original recipes and latest failed artifact, new task IDs and protected policy reuse.
- [x] Add negative tests for policy/recipe/certificate/source/record drift, forged plan fields, unbound failures, two-round limits and unchanged uncertain-signing behavior. Verify ordinary repair remains green.
- [x] Run focused Go tests and inspect fresh state; no Git mutation.

### Task 2: Worker, persistence and native acceptance

**Files:** Create `internal/worker/android_signing_repair_test.go`, `internal/platform/android_signing_repair_postgres_test.go`; extend `scripts/verify-trusted-android.go` with `--exercise-repair` to share its existing setup and exact-package readback.

- [x] Add a real HTTP/Git Worker scenario using deterministic code and a fixed signer fixture; assert actual bytes, fresh operation keys, exactly one dispatch per signed package and no unsigned QA.
- [x] Add PostgreSQL concurrent lease, replay and archive/restore checks for the repair records using an isolated new cluster.
- [x] Adapt opt-in native acceptance: introduce a release-only empty-title UI defect through an approved deterministic code recipe, record the real failed emulator run, repair the source, rebuild and sign with a fresh temporary certificate, then verify the exact installed final APK and source tree.
- [x] Preserve all acceptance logs, APK/report digests, failure and success state and cleanup evidence. Explicitly report zero model calls, releases and production operations.

### Task 3: Verification and delivery

**Files:** Update `docs/android-signing.md`, `docs/validation.md`; create `docs/android-signing-repair.md`; complete canonical todo017 after verification.

- [x] Run full Go race with PostgreSQL, vet, platform build and module tidy-diff; run Python regression if affected and existing browser regression for the defect/quality flow.
- [x] Review authority lifecycle, recursion, bounded repair, replay/expiry and permissions in the main thread; resolve any findings and rerun affected checks.
- [x] Stop owned SDK/emulator/ADB/PostgreSQL resources; verify all original resources, hashes and index records against the captured baseline.
- [x] Export a Chinese report, runbook, evidence, platform/sample source and native final-package artifacts under a new outputs prefix; independently read back all exports and exact source files/modes.
- [x] Close only todo017 once every item passes. Keep the overall product/P3 goal active with explicit external gaps.

## Completion evidence

Increment017 completed after native acceptance and independent readback at work/android-signing-repair-smydl_pp. Final Go race453/1664,Python81,browser50+16;8 actual Worker runs,one expected initial QA failure then verified repair. Exported471 platform sources,139 sample Git files/modes and10 deliverables;209 prior outputs/index/preview/Docker/AVD preserved. No paid model/store/production/commit actions. Apple SDKs still license69; true device,enterprise identity,Windows/Apple native,store and production acceptance and overall P3 remain open. Completion-only documentation is re-exported and read back after marking this item.
