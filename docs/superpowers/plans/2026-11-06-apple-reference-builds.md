# Apple Reference Builds Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in the current thread. AGENTS.md requires main-thread execution; do not dispatch agents, commit, push or alter staging.

**Goal:** Add checked, traceable iOS simulator and unsigned macOS reference builds without conflating diagnostic artifacts with signed releases or device acceptance.

**Architecture:** Extend the portable generated application's existing build helper. Apple prerequisites fail before dependency/build operations; each successful build records Xcode/SDK, bundle identity, actual executable architectures and unsigned status. Bundle archives preserve only links resolving within the application and publish with exclusive creation.

**Tech Stack:** Python standard library, pinned Flutter/Dart SDK, process-selected Xcode and xcrun.

**Spec:** `2026-11-05-reference-client-builds.md` and `../specs/2026-11-05-intelligence-substrate-design.md`.

## Global Constraints

- Preserve all 113 existing deliverable SHA256s, original 18080/PID 36063, SDKs, credentials, databases and source staging.
- Execute host shell/verification commands through `rtk`; task-owned projects, logs and caches stay in ignored `work/`.
- Do not accept Xcode/SDK agreements for the user, modify global Xcode selection, invoke release approval, call paid models or submit to a store.
- iOS simulator and unsigned macOS bundles are diagnostic artifacts. Physical device, native UI, signing, notarization, installation and store release remain separate evidence.

### Task 1: Apple build and packaging behavior

**Files:** `scripts/build-reference-client.py`, `scripts/test_build_reference_client.py`.

**Interfaces:** New `--target ios-simulator|macos-unsigned` and optional `--xcrun PATH`; existing Web/Android inputs remain supported. Output remains one immutable file plus `.json` provenance.

- [x] Write subprocess-level regressions for Apple license failure before Flutter, simulator/macOS commands, actual bundle bytes/metadata/architectures, unsupported platforms, unsafe links, signing mismatch and failed native build. Confirm red against current helper.
- [x] Implement Xcode/SDK preflight; use simulator debug/no-codesign and macOS release config-only plus explicitly unsigned xcodebuild, without team selection or provisioning updates.
- [x] Validate application metadata, executable, platform and unsigned top-level bundle; preserve internal framework symlinks and executable modes, reject links escaping the bundle and special entries.
- [x] Verify all helper/generator tests including existing Web/Android behavior and failed builds without published artifacts.

### Task 2: Current blueprint and real prerequisites

**Files:** Task-owned verification scripts/projects under `work/apple-reference-*`; blueprint README, `docs/validation.md`.

**Interfaces:** Generate new feedback/booking projects using current blueprint and exact pinned SDK, then check analyze/tests against both. The helper performs native builds only when the user-accepted Xcode license and required SDK are available.

- [x] Regenerate two isolated full-platform projects; compare copied helper/blueprint inputs, run real Flutter analyze/tests and backend tests; preserve exact logs and source/tool digests.
- [x] Execute actual helper preflight for both Apple targets. If license remains blocked, record exit 69 and no artifact; do not claim native build success from test doubles.
- [ ] When prerequisites permit, build/archive both modes for both Apple targets and independently read back actual bundles and manifests. Otherwise keep this acceptance open with the concrete blocker.

### Task 3: Review and reviewable delivery

**Files:** README and validation documentation; new source/report/runbook/evidence deliverables under the authorized outputs directory.

- [x] Review current changes sequentially for failed commands, truthful metadata, archive traversal/link safety, exclusive output creation and unchanged prior targets. Run final appropriate checks; do not rerun unrelated Go/UI suites for a Python-only change.
- [x] Export source and evidence under new names; independent byte readback and preservation checks for all 113 prior outputs and original preview. Record exact native/signing/device gaps. Overall goal remains active.

## Evidence checkpoint

`work/apple-reference-xosqz4zt/real-checks.json` records the current blueprint generation, exact pinned SDK, 30 Python tests, 26 Flutter tests and 2 backend race tests per generated project. Both projects' actual iOS simulator and macOS invocations exit 69 at preflight; no native artifact/manifest exists. The first generic Flutter version query also exposed Xcode's effect on Apple's Git shim; selecting the existing Command Line Tools for non-native checks fixed that environment boundary without changing global selection or bypassing the native Xcode license.

Native successful build/readback in Task 2 stays open until the user-accepted license and required SDK permit an actual run. Later signing, native UI/device and formal release requirements are unchanged.

Both actual release Web archives were rebuilt using the new helper and every archived file was read back against the build directory (`web-compatibility.json`, 56 entries per application). This is a compatibility build, without a new browser acceptance claim. Final portable Python tests pass 30/30; tests and generated helpers do not require RTK to be installed in enterprise CI. The local verification commands use RTK as requested.
