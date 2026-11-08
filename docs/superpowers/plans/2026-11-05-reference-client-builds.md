# Reference Client Builds Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan task by task, sequentially in the current thread as required by AGENTS.md.

**Goal:** Generate, build and exercise both reference clients with real APIs; preserve reproducible artifacts and distinguish local builds, browser/emulator checks, signing and formal publication.

**Architecture:** Improve the shared Flutter + Go blueprint, then generate isolated feedback and booking projects from that source. Inject the HTTP transport at the client boundary for deterministic widget tests; exercise production Web builds against actual Go processes separately. Build commands use explicit task-owned caches and the existing Android SDK.

**Tech Stack:** Fixed official Flutter source/SDK, Dart, existing Android SDK/JBR, Go, Chrome/Playwright, Python.

**Spec:** `docs/superpowers/specs/2026-11-05-intelligence-substrate-design.md`, `docs/reference-apps/customer-feedback.md`, `docs/reference-apps/service-booking.md`.

## Global Constraints

- Required targets remain Web, iOS, Android, Windows and macOS; first channels remain App Store, Google Play, Huawei, Xiaomi, OPPO, vivo and Web/desktop direct distribution.
- Real browser, simulator, physical device, signed build and store publication are separate evidence categories.
- Every shell command starts with `rtk`; execute sequentially without subagents; do not commit or push.
- Preserve the running platform preview, user credentials, existing Android SDK and unrelated services.
- Keep SDK, generated projects, caches, temporary API data and logs inside ignored `work/`; do not change global tool selections or accept legal agreements on the user's behalf.

### Task 1: Reproducible generated projects

**Files:** `scripts/new-app.py`, `scripts/test_new_app.py`, `blueprints/flutter-go/client/pubspec.yaml`, `blueprints/flutter-go/client/test/`.

**Interfaces:** `new-app.py DEST --name NAME --mode feedback|booking --organization DOMAIN [--create-platforms]` produces a self-contained client/backend without overwriting another project.

- [x] Establish a fixed Flutter release or exact official source commit, record integrity metadata, and bootstrap within `work/`.
- [x] Add a generator regression that executes the script against a controlled Flutter scaffold, verifies generated tests compile against the selected package, and rejects overwriting existing destinations.
- [x] Observe the regression fail before fixing scaffold/test handling; then verify both modes preserve custom sources and portable tests.
- [x] Generate two fresh projects and run dependency resolution, `flutter analyze`, widget tests and backend race tests.

### Task 2: Reliable client interactions

**Files:** `blueprints/flutter-go/client/lib/main.dart`, `blueprints/flutter-go/client/test/widget_test.dart`.

**Interfaces:** `DeliveryApp` accepts optional `http.Client`, API base URL and application mode, with compile-time production defaults. Requests preserve server validation/conflict errors, never retry writes automatically, and release pending state safely when a view disappears.

- [x] Write failing widget tests for empty/UTF-8 overlimit feedback, one in-flight write, refresh recovery after a network failure, booking conflict and invalid cancellation, token visibility, and disposal during a request.
- [x] Implement input validation, explicit pending/error states and mounted checks. Keep cancellation credentials in the user's active view after successful reservation; server persists only their hashes.
- [x] Verify widget tests and analyzer; regenerate clean projects from the corrected blueprint.

### Task 3: Actual builds and business acceptance

**Files:** `scripts/verify-reference-apps.mjs`, `scripts/build-reference-client.py`, `blueprints/flutter-go/blueprint.json`.

**Interfaces:** Build helper accepts an explicit generated application path, target and API URL; runs checks/builds and writes one immutable archive plus JSON provenance (source digest, SDK, command, archive SHA256). Browser verifier launches isolated actual APIs/static clients and records outcomes, screenshots and backend restart persistence.

- [x] Add failing behavioral tests for build-helper input validation, failed-command propagation and refusal to overwrite artifacts before implementing the helper.
- [x] Build release Web archives for both applications and exercise submit/list/plain-text, offline recovery and duplicate-click behavior against their actual backends in Chrome.
- [x] Exercise booking ownership/conflict/cancel and verify JSON data through backend restarts.
- [x] Build Android APKs with explicit process-only SDK/JBR configuration; install and exercise on an isolated emulator when available. Record blockers without mislabeling unsigned/debug-signed packages as publishable.
- [x] Attempt native Apple prerequisites read-only; preserve any actual license/account blocker. Windows native builds require a Windows node.

### Task 4: Evidence and product integration

**Files:** Blueprint README, two reference specifications, `docs/validation.md`, `docs/workers.md`, user-facing continuation report and evidence manifest.

- [x] Document reproducible toolchain/build/worker commands and archive paths. Confirm build helper works from a worker's repository root and outputs a single uploadable file.
- [x] Run appropriate fresh source checks; compare preview resource/credential summaries if any platform restart is needed.
- [x] Save the continuation report and real screenshots under the authorized outputs directory, stating completed checks and every unresolved native/signing/channel requirement.

The complete product goal remains active after this increment; these checks cannot satisfy the two-app all-channel P3 acceptance by themselves.

### Task 5: Close the measured public-font dependency

The final cold-browser network probe (`work/reference-font-network-probe.json`) observed implicit `fonts.gstatic.com` requests even with local CanvasKit. This prevents claiming a self-hosted client resource bundle.

- [x] Reproduce the outbound requests with external origins blocked before changing the client.
- [x] Vendor unchanged official Noto Sans SC and Roboto fonts with licenses, exact upstream commits, Git blob checks and SHA256 provenance.
- [x] Add and observe a failing generator regression before preserving the custom Web bootstrap through scaffolding.
- [x] Use bundled font families and a local fallback base; make the browser verifier reject undeclared external origins.
- [x] Regenerate and build both Web/Android clients, rerun browser and emulator acceptance, and repeat actual worker builds for the revised source.
- [x] Update final artifact digests, screenshots, evidence paths and deliverables for this revision.

## Evidence checkpoints (2026-11-06)

- Toolchain source integrity: `work/toolchains/flutter-source-verified.json`; Flutter 3.47.9 / Dart 3.13.7 at exact upstream commit. Full SDK ZIP was not completed/verified.
- Red/green client evidence: `work/reference-client-behavior-red.log`, `work/reference-client-green.log`; final generated build logs run all 9 widget tests per app again.
- Final generated applications: `work/native-reference-local-fonts-5g1xtc30`; explicit Gradle 9.4.1 profile with a copied existing installation and file-level provenance, not a newly verified original ZIP.
- Final Web business acceptance: `work/reference-browser-XYLOp2/result.json`, 6 scenario groups against the actual packaged Flutter clients and real Go APIs, including an actual successful read after network recovery/restart and zero undeclared external requests. The preceding `work/reference-browser-Kx4dDO/result.json` also passed after resolving the observed DOM-focus/text-editing activation race in the verifier; no client changes were needed.
- Final Android business acceptance: `work/reference-android-14donm30/result.json`, 4 scenario groups on the dedicated arm64 emulator, actual APK signatures and APIs. No physical/store claim.
- Actual control-plane/worker integration: `work/reference-worker-3363596321/result.json`; both imported source commits built/uploaded and passed independent automated archive checks. Browser release gate still rejects these separate HTTPS-example artifacts.
- Source checks: 15 Python tests, Dart format, generated source/lock comparison (allowing only Flutter's extra platform analysis exclusions), script syntax, local links, CI YAML and diff whitespace. No platform restart or remote CI in this increment.
- Apple: process-only `DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer xcrun simctl list devices --json` still exits 69 until the user personally accepts the Xcode/SDK license. No agreement accepted or global tool selection changed.
