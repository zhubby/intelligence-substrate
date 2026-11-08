# OPPO Publisher Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in this thread. User AGENTS.md overrides delegation; preserve unborn feature branch/index and do not commit or push.

**Goal:** Update approved enterprise ordinary applications through OPPO with verified bytes, durable intents and accurate asynchronous/version observations.

**Architecture:** Administrator profile and reviewed local assets feed an isolated HTTPS client. SQLite serializes credential-token issuance and package write ownership; exact task/version status is independent of platform post-publication acceptance.

**Tech Stack:** Go standard library HTTP/HMAC/SHA256/MD5/multipart, SQLite, existing Android SDK verifier and release control plane.

**Spec:** `../specs/2026-11-06-oppo-publisher-design.md`.

## Global Constraints

- Main thread, RTK, no external store mutations/paid models/staging/commit/push.
- Preserve 121 original outputs, preview 18080/PID 36063, existing state and SDKs.
- Fixed android-oppo/oppo-store ordinary existing application; no first-app API promise or package pre-recording substitute.
- Unknown mutation retains package reservation; official API acceptance never proves publication or independent device acceptance.

### Task 1: Fixed profile and reviewed material

**Files:** `internal/publisher/oppo_material.go`, `internal/publisher/oppo_test.go`, `internal/publisher/publisher.go`.

**Interfaces:** `validateOppoProfile(Profile) error`, `stageOppoMaterial(Profile) (*oppoMaterial,error)`; metadata binds typed application fields and local icon/screenshots/copyright/special images.

- [x] Add failing tests invoking real `Open/Execute`: profile rejected/accepted, bad signer/digest/material/asset/URL fail before any network or operation.
- [x] Run `rtk proxy go test ./internal/publisher -run TestOppo -count=1`; confirm unsupported driver failure, then implement profile/material/SDK integration.
- [x] Verify icon/screenshot dimensions, complete image decoding, strict JSON duplicates/unknown fields, metadata SHA256 and attachment size/digest staging.

### Task 2: Official protocol and durable submission

**Files:** `internal/publisher/oppo_protocol.go`, `internal/publisher/oppo.go`, `internal/publisher/oppo_test.go`, `internal/publisher/publisher.go`.

**Interfaces:** `executeOppo(ctx,p,r,hash) (Result,error)`; `Receipt.Oppo` contains only stage, verified version and APK MD5, no tokens/remote URLs. SQLite oppo_destinations and oppo_tokens hold exclusive mutation/cache state.

- [x] Add local TLS fixture independently computing HMAC from decoded raw form parameters, observing actual bytes/Content-Length, isolated upload credentials, pre-write persisted intent and token caching across reopen.
- [x] Observe failing protocol tests, then implement exact fixed API endpoints, administrator host allowlist, disk-backed non-replayable uploads and bounded strict replies.
- [x] Exercise 1 token for repeated queries; expired/invalid/duplicate token errors, URI/cookie/redirect isolation; verify 6 approved files use 6 one-time upload tickets before one app/upd.
- [x] Exercise lost response, API/HTTP/duplicate success failure at each mutation, upload digest mismatch, concurrency/reopen and local completion failure. Confirm no automatic replay and locks persist when uncertain; success result and lock release are atomic.

### Task 3: Accurate observations and delivery gates

**Files:** `internal/publisher/oppo_status.go`, `internal/publisher/oppo_test.go`, `internal/platform/oppo_delivery_test.go`.

**Interfaces:** `oppoStatus` performs POST read-only task query then GET exact-version info. Local ID `oppo:<appId>:<versionCode>` cannot change binding.

- [x] Add failing task state 1/2/3 and precise audit fixtures. Test old shelf flag, mismatched app/package/version/hash/size/version_id, phased/frozen/transferred and unknown state never claim published.
- [x] Implement supported status mapping; unknown states require manual verification, no payload/free-text leak, repeated status never mutates journal or store.
- [x] Verify actual control-plane transitions release leases while waiting; published still rejects own/pre-release/incorrect evidence and accepts only independent exact publication evidence (synthetic attestation explicitly labeled).

### Task 4: Verification and handoff

**Files:** deploy OPPO examples; `docs/oppo-store.md`, README/releases/workers/validation; source/report/runbook/evidence under outputs.

- [x] Document first creation/publication in portal, current official doc links, exact material/credential configuration, supported states and uncertainty runbook.
- [x] Run full Go vet/race with new dedicated PostgreSQL; compile seven programs for Darwin arm64/Linux amd64/Windows amd64; actual SDK/publisher CLI wrong-signer, Debug, digest and status-only rejection.
- [x] Main-thread correctness/permissions/replay review; fresh Apple prerequisite probe; preserve 121 digests/index/preview. Export and independently read source/log/output bytes; leave whole product/P3 active.

## Completion evidence

Full vet/race: 266 top-level / 775 including subtests, zero failures/skipped tests; OPPO 16/129 included. Dedicated PostgreSQL 17.6 loopback 50285 stopped. 21 binary builds and four actual SDK/CLI rejection cases passed. Current official documents 10998/10999/11003/11004/11001/11176/12442/13438 fully decoded and verified. Regression/CLI logs: work/oppo-publisher-s4fao3p7/{regression/regression-result.json,cli/cli-evidence.json}. Export/readback completes the deliverable check; 121 original outputs, preview PID 36063 and staging preserved. Apple SDK exit 69; real signer/store/device and whole-product/P3 acceptance remain open.
