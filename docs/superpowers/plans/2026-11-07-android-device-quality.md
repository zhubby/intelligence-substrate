# Android Physical-device Quality Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially; user instructions prohibit subagents and Git staging/commits.

**Goal:** Supply the missing trusted Android USB-device business verifier, preserving exact artifact, identity, independent-quality and truthful evidence boundaries.

**Architecture:** A small CLI calls `android_device_quality.py`. Existing APK/source/install safety helpers move unchanged into `android_quality_common.py` and stay re-exported by the emulator adapter. Native tools and devices are external dependencies; a dedicated HTTPS backend is operator-provided. No control-plane schema or release-gate changes.

**Tech Stack:** Python3 standard library, existing Go/Flutter reference API, trusted Android SDK ADB/aapt/apksigner, protected quality Workers.

**Spec:** ../specs/2026-11-07-android-device-quality-design.md

## Global constraints

- Main-thread sequential execution; preserve861 initial sources/379 historical outputs/261 index entries, zero commits and original preview/Docker/AVD/keychains.
- No paid model, actual store/production operation, physical-device authorization or Apple license acceptance.
- First blueprint is flutter-go-v1 feedback/booking and arm64 release APK; successful fixture runs remain synthetic hardware evidence.
- New physical evidence must be acquired on an actual administrator-bound USB device. Independent quality, exact commits and protected criteria remain unchanged.

## Task1: Device identity and exact-package admission

**Files:** create `scripts/test_android_device_quality.py`, `scripts/android_device_quality.py`, `scripts/verify-reference-android-device.py`, `scripts/android_quality_common.py`; modify `scripts/verify-reference-android-quality.py` to re-export extracted helpers.

**Interfaces:** `load_profile(path) -> dict`; `physical_identity(adb, profile) -> dict`, where `adb(*argv) -> str`; `validate_release(badging, signature, profile) -> dict`; `run_business(ui, api, profile, report) -> None`, where UI exposes `find/tap/enter/launch/capture` and API exposes `request(endpoint, body=None, expected=200)`.

- [x] Write admission tests that reject an emulator/ambiguous/offline/changed device before mutation, reject profile/HTTPS/signature/version mismatches and retain an existing package. Test TLS downgrade rejection against a real local HTTPS server; capture expected failures before implementation.
- [x] Extract existing safety helper functions without changing their semantics, implement the profile/device/package admission interfaces and run new tests plus the existing emulator safety tests until green.

Example test contract:

```python
with self.assertRaises(ValueError):
    physical_identity(adb_for_emulator, profile)
# The real CLI integration also asserts the preinstalled APK bytes remain intact.
```

## Task2: Business verification, ownership and protected reports

**Files:** extend the core/test files; create `deploy/quality.android-device.example.json` and `docs/android-device-quality.md`; update README, product-acceptance, validation and master plan.

- [x] Add failing business tests: actual feedback writes must match both native UI and HTTPS API; booking conflict, wrong-owner cancellation and valid cancellation must preserve the correct slot state. An API error/unknown write must fail without write replay.
- [x] Implement the dedicated API/UI runner, exact install-byte check, final identity/byte recheck and owned-package cleanup. Interrupted installation, changed bytes/identity and cleanup failure must retain appropriate failure records. Report hard-link publication refuses existing output.
- [x] Run the actual CLI with controlled SDK/device processes against a real TLS/reference API and verify archive/source/package/checks/cleanup independently. Run real SDK/default-ADB negative checks without touching a device. Use only a dedicated process/TLS fixture, never claim physical success.
- [x] Run Python regression appropriate to the shared helper extraction and existing protected-quality Go tests. Confirm all unchanged Go/frontend inputs still match050; do not rerun unrelated full Go/Flutter/browser suites.
- [x] Export source/report/runbook/public evidence; verify all source bytes/sizes/modes, original state, historical outputs and explicit synthetic/physical evidence boundaries. Close051 only after readback; keep overallP3 incomplete.

## Progress

- 2026-11-07: Previous goal turn completed050 acceptance-audit documentation. Captured fresh051 baseline before product changes. Remaining work is an implementation prerequisite for original Android physical-device acceptance; no claim that the unavailable device has passed.

- 2026-11-07: Implemented and reviewed sequentially. Python190, affected Go race20/67, six retained CLI/TLS/Go process cases, exact real-SDK denial and raw source/APK/profile/business/cleanup readback pass. First output acceptance passes873-source/874-entry ZIP bytes/sizes/modes, documentation links and all379 prior outputs.855 inherited sources remain unchanged;261 index entries including flags, zero staged changes/commits and original preview/Docker/AVD/keychains preserved. Only051 completes; final export/readback includes these records. Real hardware, identity/signing, native builds, stores/production and P3 remain unaccepted.
