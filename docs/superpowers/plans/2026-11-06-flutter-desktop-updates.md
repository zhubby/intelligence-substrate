# Flutter desktop update integration

> Execute sequentially in the main thread using executing-plans. The approved product plan already authorizes desktop update delivery; no delegation, commit, signing or installation is authorized by this continuation.

**Goal:** Connect the existing governed updater to the two Flutter application modes, with exact-package consent and truthful recovery states.

**Architecture:** A conditional Dart factory keeps Web/mobile independent of dart:io. A native process bridge invokes only an administrator-provisioned absolute executable/profile, without a shell or inherited credentials. A typed snapshot and one application-scoped controller drive a dedicated update dialog; cryptographic/native trust and durable installation reservations remain in the existing Go CLI.

**Tech stack:** Existing Flutter/Dart SDK, standard dart:io processes and existing Go updater; no new runtime dependency.

**Spec:** ../specs/2026-11-06-desktop-updater-design.md and the approved platform product specification.

## Constraints

- Entry is opt-in at build time with fixed absolute UPDATE_EXECUTABLE and UPDATE_PROFILE. Web, Android, iOS and unsupported systems have no process update entry.
- Status/check/stage never trigger install. Confirming a dialog approves its captured SHA256, never a later candidate. Cancel/disposal before consent cannot dispatch installation, and repeated clicks cannot dispatch extra installations.
- Only one process operation per application controller. Bounded stdout/stderr are drained until process exit; closing the dialog does not kill or retry an in-flight installer.
- Installation errors, unknown output or loss of process communication force a fresh status before another action. An authoritative installing/reconciliation_required state permits only status. Reconcile stays an administrator CLI operation.
- Install consent warns about application shutdown and saving work. No sudo/elevation, agreement acceptance, reset, forced retry or automatic restart.
- Actual CLI status can be tested without installation; OS signing/install success remains external acceptance.

## Tasks

- [x] Typed bridge: first write Dart tests for action arguments, invalid configuration/JSON, nonzero-exit snapshots, concurrent operations and bounded output. Observe failures; implement model, conditional factory and process bridge. Use actual child processes for transport tests.
- [x] Application flow: write widget tests for opt-in entry, captured-digest confirmation/cancel, double click, pending route behavior and uncertain/restarted status. Implement the update dialog and integrate both application modes. Keep UI strings about versions/actions, not internal paths.
- [x] Verification and delivery: run Flutter analyze/all tests, Python generator regressions, real updater process status, compile a Web release with the bridge excluded, inspect generated application preservation, document the configuration and boundaries, export current source and evidence without replacing previous deliverables.

## Files

- Create `blueprints/flutter-go/client/lib/updates/{model,desktop_updater,process_updater,unsupported_updater,update_dialog}.dart` and focused tests under `client/test/`.
- Modify `client/lib/main.dart` to keep one updater for the Home lifecycle and add the opt-in application update entry.
- Add a standalone Dart CLI bridge probe under `client/tool/` for actual executable/status acceptance. It must not offer installation.
- Update README, blueprint manual, desktop update runbook, validation matrix and original implementation ledger.

## Verification commands

Run using `rtk proxy`: the task-local `work/toolchains/flutter/bin/flutter analyze --no-pub`, `flutter test --no-pub --reporter expanded`, Python unittest discovery, `go test -race ./internal/updater ./cmd/updater`, and a fresh generated Web release using `scripts/build-reference-client.py`. The bridge probe must spawn the current compiled Go updater against a fresh valid profile and verify `unchecked` across separate processes; it never checks a remote feed or installs a package.

## Evidence

`work/desktop-ui-9x61w94z/result.json` records successful generation, analyze and 26 Flutter tests per application, two actual Web builds, 18 Python tests, 20 top-level/47 Go tests including subtests, and two real status-only Go CLI invocations through Dart. `work/reference-browser-76Dgmn/result.json` records six actual Chrome/Go business groups and five absent native-update entry observations.

The generator also has an explicit macOS direct-distribution option, tested with default-sandbox preservation and recorded opt-in metadata. Native desktop signing, packaging, installation and business acceptance remain open; the scoped checklist does not mark P3 or the overall product plan complete. Main-thread review and export read-back are retained under ignored `work/` evidence.
