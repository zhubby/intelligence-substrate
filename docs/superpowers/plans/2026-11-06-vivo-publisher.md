# vivo Publisher Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in the current thread. User AGENTS.md prohibits delegation. Preserve the unborn branch/index; no staging, commits or pushes.

**Goal:** Add reviewed, durable vivo standard-application updates and truthful read-only observations without inventing store publication evidence.

**Architecture:** Fixed administrator environment/profile → SDK and material preflight → signed read-only prerequisites → transactional package/version ownership → write-ahead file intents → synchronous update → atomic receipt. Existing release observation and independent acceptance gates remain authoritative.

**Tech Stack:** Go standard crypto/HTTP/multipart/images, existing SQLite journal and Android SDK verifier.

**Spec:** `../specs/2026-11-06-vivo-publisher-design.md`.

## Global Constraints

- Run host commands through RTK; verification state stays in ignored `work/vivo-publisher-*`.
- Preserve 125 original output SHA256s, index, PID 36063/18080 and unrelated SDKs/databases.
- No paid inference, real store write, accepted agreements, production approval or native/device success claim.
- Production and sandbox use separate fixed origins, credential files and durable destination/version namespaces.
- No automatic replay/recovery/unlock or new frontend scope.

### Task 1: Profile, reviewed material and wire contract

**Files:** `internal/publisher/{publisher.go,vivo_material.go,vivo_protocol.go,vivo_test.go}`.

**Interfaces:** `Profile.Environment`; `validateVivoProfile(Profile) error`; `stageVivoMaterial(Profile) (*vivoMaterial,error)`; `Engine.vivoClient(Profile) (*vivoClient,error)`; `vivoClient.call(context.Context,string,url.Values,*vivoFile,any) error`.

- [x] Add fixture/test through existing `Open`/`Execute` asserting an official signed update is awaiting_review. Watch the existing driver rejection before implementation.
- [x] Add strict fixed-environment validation and private manifest/credential checks. Share existing duplicate-JSON scanning and bounded image staging; wrap errors with vivo context. Require exact metadata bytes, reviewed text limits, device/age values, HTTPS privacy and optional validated image groups.
- [x] Implement `app.query.details`, `app.upload.apk.app`, `app.upload.icon`, `app.upload.screenshot`, `app.sync.update.app` only. Sign raw values, millisecond timestamps, disk multipart and UTF-8 parts. Require HTTP 200, bounded unambiguous JSON, code=0/subCode="0", exact upload identities. Test both API and business failures, redirects and malformed data without replay.

### Task 2: Persistent operation, package and version state

**Files:** `internal/publisher/{publisher.go,vivo.go,vivo_status.go,vivo_test.go}`.

**Interfaces:** `Receipt.Vivo`; `Engine.executeVivo(context.Context,Profile,Request,string) (Result,error)`; `Engine.finishVivo(context.Context,Request,string,*vivoReceipt,Result) error`; `vivoClient.details` and `editable`.

- [x] Add tests for same/new key, close/reopen, concurrent execution, package contention and failed checkpoints before implementing SQL ownership. Use `operations`, `vivo_destinations(environment,package_name,operation_key)`, `vivo_versions(environment,package_name,version_code,operation_key)`.
- [x] Reserve all ownership in one transaction, recheck editable state, persist each upload/update intent. Confirm APK response package/version/name/MD5; retain only serials and non-sensitive identity. Final SQL must atomically update complete receipt/result and delete exactly one owned lock; a local failure leaves pending and forbids replay.
- [x] Test stale upstream after accepted update and permanent same-version ownership. Return original successful result without network for exact replay; reject binding/environment/material and externalId changes.
- [x] Test each exact-version observation: review, rejection, release wait, offline and manual ambiguous status; never emit published, never use old/unrelated app values or arbitrary upstream strings as proof.

### Task 3: Control plane, operator setup and final evidence

**Files:** `internal/platform/vivo_delivery_test.go`, `deploy/{publisher.vivo.example.json,worker.vivo.example.json,vivo-metadata.example.json}`, `docs/vivo-store.md`, README/release/worker/validation docs, canonical todo.

**Interfaces:** Existing `releases.dispatch/refresh/transition/verify` and Worker trusted publisher protocol; no new API action.

- [x] Exercise real SQLite commands: upload → review → manual required with no execution lease or polling → human portal proof → independently bound publication evidence. Reject Worker/human without proof, old QA and self acceptance. Samples explicitly synthetic.
- [x] Add production example with placeholder private paths/approved digest, sandbox selection instructions, official links, current API ambiguities, supported material subset, receipt IDs, persistent uncertain-operation runbook and real-account gaps.
- [x] Sequential correctness/security/simplicity review; fix actionable issues and add a failing regression first. Run targeted tests, full vet/race with task-owned PostgreSQL, module/diff/gofmt checks and seven programs on three platforms (21 builds).
- [x] Actual SDK/new CLI rejection checks using an existing diagnostic APK, no credentials or network writes. Verify zero operation/locks/version reservations. Recheck Apple SDK preflight without accepting license.
- [x] Export new source/report/runbook/evidence under outputs with exclusive creation. Independently read back source/ZIP/logs/125 preserved outputs/index/PID/cluster shutdown; do not mark overall P3 complete.

## Verification checkpoint

`work/vivo-publisher-x4ix5zds`: complete vet/race 282 top-level / 918 including subtests (vivo 16/143), 21 program builds, dedicated PostgreSQL 17.6/56944 stopped, four real SDK/CLI rejections with zero operations/package/version records. Source/output export and independent readback are recorded by the task-owned export scripts; no live sandbox/store/signature/device or P3 completion claim.
