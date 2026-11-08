# Android App Bundle Implementation Plan

> **For agentic workers:** Execute sequentially with superpowers:executing-plans. The user prohibits subagents, staging, commits and pushes.

**Goal:** Build, sign and independently validate traceable Google Play AAB packages through the platform and record honest native/store boundaries.

**Architecture:** Reuse governed Android signing and repair; extend fixed administrator tools with format-specific AAB validation/JAR signing. Independent reports bind the exact bundle; derived emulator APKs carry separate provenance.

**Tech Stack:** Go/SQLite/PostgreSQL, Python, Java JarFile/jarsigner, pinned bundletool1.18.3, existing Flutter/Android SDK.

**Spec:** `docs/superpowers/specs/2026-11-06-android-aab-design.md`.

## Global Constraints

- Preserve471 original source files,219 prior outputs,261 Git index records,zero commits,original preview/Docker/AVD. All generated tools, keys, caches and evidence stay in ignored work/android-aab-42qn4fi8; deliverables only in the authorized outputs directory.
- No paid models, real store/production actions, Apple agreement acceptance, global developer selection, subagents or Git staging/commit/push.
- Explicit android-google-play/android-aab mapping; existing domestic APK mappings remain. Maximum two repair rounds; never clear/retry a signing intent.
- CI bundle verification, locally derived APK installation, physical devices and Play distribution have separate completion semantics.

### Task1: Controlled format and Worker routing

**Files:** Modify platform/Worker Android signing and test fixtures; add AAB tests.

**Interfaces:** Existing Android signing input/output schema is retained; target uniquely fixes mode/format. `androidSigningTask`, task validation, upload/completion and repair recognize android-aab only on android-google-play. Worker uses unsigned.aab/signed.aab with the same fixed signer boundary.

- [x] Add failing AAB planning/signing/quality and repair tests using real service operations; reject APK/AAB target swaps and forged fields.
- [x] Observe `rtk proxy go test ./internal/platform ./internal/worker -run 'AAB|AndroidSigning' -count=1` fail on unsupported mode before changing production code.
- [x] Implement target/mode mapping, server artifact metadata and Worker filenames; preserve existing unknown-signing/retry behavior.
- [x] Run focused tests and actual HTTP/Git Worker fixtures for both formats.

### Task2: Real unsigned bundle and fixed signing program

**Files:** Add scripts/android_bundle.py, VerifyAndroidBundle.java and Python tests; modify build-reference-client.py and sign-android-apk.py; add AAB administrator config example.

**Interfaces:** Bundle helper exposes bounded archive inspection, pinned bundletool metadata and Java signature verification. Builder accepts android-unsigned-aab and explicit trusted tool paths. Signer selects format from the fixed target and persists the original schema/operation journal before jarsigner.

- [x] Add failing structure/manifest/signature/payload and build-helper tests; retain red logs.
- [x] Implement strict ZIP/manifest checks, all-entry Java cryptographic verification and format-specific signing. JAR signing metadata alone may change, not application payload.
- [x] Verify successful same-key replay, wrong cert/input/unsigned entry rejection, private-byte absence and no automatic replay of interrupted intents.
- [x] Run actual Flutter analyze/tests/AAB build and real jarsigner/bundletool checks with a new temporary upload identity.

### Task3: Independent evidence and native acceptance

**Files:** Add bundle quality report tool and native acceptance entry; reuse actual Android emulator verifier.

**Interfaces:** Quality command reads IS_ARTIFACT_PATH, validates exact signed AAB and source tree, writes one immutable tar report. Native acceptance derives a universal APK using a separate local install certificate and records exact mapping/install digest.

- [x] Run real platform build/sign/independent quality through HTTP/Git Workers; unsigned input must never dispatch final QA.
- [x] Verify the 140-file source tree, signed/unsigned payload identity, exact durable signing intent and automatic quality binding. Attempt release creation and assert missing android-device evidence leaves zero candidates.
- [x] Derive/install/test the exact local universal APK on a new isolated AVD copy; label its certificate and bytes separately from upload bundle/Play distribution. Stop only owned resources.

### Task4: Regression and deliverables

**Files:** Update Android signing/repair/API/validation docs, README; add docs/android-aab.md and canonical todo018.

- [x] Run full Go race with fresh PostgreSQL, vet/build/tidy-diff, Python and standard browser checks appropriate to changed paths. Review authority, format swaps, JAR payload completeness and replay in the main thread.
- [x] Verify preservation against the baseline and closed task-owned ports; do not mutate Docker or original preview/AVD.
- [x] Export Chinese report/runbook/evidence, platform/sample sources, AAB and native report/screenshots without private material. Independently read back all deliverables and exact SQL/artifact/source hashes.
- [x] Complete only todo018 after verification; keep overall P3 active with actual Apple/license/Windows/device/identity/store/production gaps.

## Evidence checkpoint — 2026-11-06

Evidence root: `work/android-aab-42qn4fi8`.

- `actual-platform-final/result.json`: three successful real HTTP/Git Worker executions (unsigned build, dedicated AAB signing, protected independent QA); zero persisted releases, with missing physical-device evidence explicitly rejected.
- `cryptography-and-source.json`: exact sample commit `3a3dc4fa5f66b98ae013119927926a8091d3fb7f`, 140 source files and 77 unchanged payload entries. Actual Java verification rejected unsigned, signed-as-unsigned, wrong-certificate, additional unsigned-entry and payload-tampering cases. Successful signing replay preserved original bytes and journal/receipt.
- `native-derived/result-final.json` and `native-final/native-report.tar.gz`: a separately signed local universal APK was installed and verified on the isolated Android 16/API 36 arm64 emulator. The final AAB and native reports contain the identical 140-file source map from a clean checkout. The initial native report's ten Git-ignored scaffold files caused export to stop; the clean-source reinstall corrected the evidence without changing the acceptance gate. Historical reports remain intact.
- `regression-complete/result.json`: final complete Go race passed 459 top-level tests / 1,687 including subtests, with zero failures/skips and isolated PostgreSQL 17.6 concurrency, replay, repair and restore. Vet, platform/Worker/acceptance builds, formatting and module tidy-diff passed. `static-checks-result.json` records 94 passing Python tests; `standard-browser/result.json` records 50 passing browser flows and 14 deterministic local-provider calls, with no paid-model calls.
- `owned-process-cleanup.json` and `preservation-result.json`: task-owned resources stopped; 484 current source files, original 219 outputs, 261 index entries, zero commits, original preview, Docker baseline and AVD retained. Additional unrelated Docker resources were observed and left untouched; complete Docker-inventory byte identity is not claimed.
- Initial `export-result.json` and `independent-readback-result.json` passed for all ten deliverables before completing todo 018. Readback checks platform and sample archives, exact Git file modes, durable SQL, signing intent, signed/installed bytes, quality reports, links and preservation. `policy-timestamp-readback.json` retains the nine-microsecond policy `updatedAt` difference; all policy authority fields and revision match exactly. Completion documentation is followed by a final refreshed export/readback.

Only the AAB increment is complete. Full P3 remains open. The sample uses temporary upload and separate local installation identities; it does not establish physical-device, live-service, Play App Signing or store/production acceptance. Both Apple SDK checks still returned license error 69. No agreement acceptance, global Xcode selection, staging, commits, pushes or preview/store/production deployment occurred.
