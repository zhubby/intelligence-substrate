# App Store Delivery Implementation Plan

> Execute sequentially with superpowers:executing-plans. The user's AGENTS instructions require main-thread execution without subagents; implementation is already authorized by the approved product plan.

**Goal:** Upload an approved iOS IPA through App Store Connect, release execution resources during processing, submit the exact processed build for review, and observe the actual version state.

**Architecture:** A fixed administrator-owned `app-store` publisher profile holds app ID, bundle ID, team and upload host allowlist. The trusted publisher journals upload/file/build/version/review IDs. The platform distinguishes `processing`, `awaiting_review` and `awaiting_release` and dispatches mutating `publish_continue` work only after a read-only observation requests continuation. Every continuation is bound to the original approval, artifact, operation and current requirement/plan gates. Unknown external writes remain locked for operator reconciliation.

**Tech stack:** Existing Go/SQLite publisher, platform and worker; native macOS codesign/plutil verification; App Store Connect HTTPS/ES256 API; React workbench.

**Spec:** ../specs/2026-11-05-intelligence-substrate-design.md, especially truthful channel states, independent validation, approval binding and bounded durable recovery.

## Global constraints

- All shell commands start with `rtk`. No commit/push, subagents, real store mutation or unrelated resource cleanup.
- Upload accepted is `processing`; only an accepted review submission is `awaiting_review`. An upstream available version is `published`, never independent device verification or delivered.
- Status providers never perform Apple writes. Continue providers are explicitly configured and never fall back to normal publishers.
- Credentials, exact destinations and upload HTTPS hosts are administrator-owned. Never forward ASC authorization or cookies to upload hosts; reject redirects, malformed/overlapping/incomplete ranges and unsafe supplied headers.
- Validate IPA hash/size, bundle/version/team, distribution signature and non-debug entitlement before reserving an external upload. Positive native signature acceptance requires real macOS credentials and is not available on this machine.
- Store listing, screenshots, privacy/export declarations, review contact and a unique matching version must already be prepared in App Store Connect. Reject absent, ambiguous or changed version/build rather than silently creating or replacing metadata.
- Journal before external mutation, checkpoint receipts after each accepted stage; an interrupted write never auto replays. Read-only polling has finite retries and releases its lease. Original approval must still be valid before continuation.
- Do not report real Apple upload/review/signature success from HTTP fixtures. Keep full product goal active.

## Tasks

- [x] **1. Processing and governed continuation.** Add `internal/platform/apple_delivery_test.go`, extend `channels.go`, `tasks.go`, `releases.go`, worker config/runner and tests. First demonstrate that a successful upload with `state=processing` fails today. Implement resource-free waiting, exact original-operation continuation, bounded polling, separate provider dispatch, stale/revoked authorization checks and interrupted-continuation reconciliation. Verify targeted platform and worker race tests.
- [x] **2. Trusted Apple transport and package verification.** Add `internal/publisher/apple_auth.go`, `apple_package.go`, `apple_upload.go`, `apple.go` and tests. Extend profile/receipt/engine. Test real ES256 signatures and strict credential validation, archive/path/size checks, unsigned rejection, host/range/header rejection and exact bytes uploaded to a TLS fixture. Run failing tests before each behavior. Official request fields are checked against Apple's cached documentation under ignored `work/apple-*.json`.
- [x] **3. Durable upload, review and observation.** Implement upload reservation, IPA file reservation, byte uploads, checksum commit, processed-build lookup, matching prepared version, linking, review submission/item and final submission. Persist stage IDs, reserve continuation with SQL compare-and-swap, reject binding/profile drift and block ambiguous writes/restarts. Verify HTTP fixture chronology, raw upload SHA256, readonly status, stale build rejection, concurrency and restart replay. No implicit resubmission on failure.
- [x] **4. Workbench, configuration and acceptance.** Show processing, channel observations and continuation work in the workbench. Add fixed App Store/worker configuration and a source-linked runbook; update API/release/validation docs. Run Go race/vet, native and cross builds, TypeScript/Vite, desktop/mobile browser checks and preview-data preservation checks. Save a concise Chinese user-facing report under the designated outputs directory, with live-account/native gaps explicit.

## Authoritative API references

- https://developer.apple.com/documentation/appstoreconnectapi/build-uploads
- https://developer.apple.com/documentation/appstoreconnectapi/post-v1-builduploads
- https://developer.apple.com/documentation/appstoreconnectapi/post-v1-builduploadfiles
- https://developer.apple.com/documentation/appstoreconnectapi/patch-v1-builduploadfiles-_id_
- https://developer.apple.com/documentation/appstoreconnectapi/review-submissions
- https://developer.apple.com/documentation/appstoreconnectapi/generating-tokens-for-api-requests

## Completion boundary

This increment does not provide signing identities, missing store materials, physical-device acceptance or a real Apple account. Those remain explicit prerequisites to formal release. The platform's original multi-store/enterprise/two-production-application objective remains active.

## Verification ledger

- All four scoped tasks implemented. 96 top-level Go tests and 37 subtests pass with race detection, no skipped tests; actual isolated PostgreSQL included. go vet passes.
- Six commands build for local macOS arm64 and Linux/Windows amd64 (18 builds). TypeScript/Vite passes.
- Four Chrome desktop/mobile scenarios pass, including separately queued review continuation and truthful button state; fixtures are synthetic, without live Apple or physical device acceptance.
- Actual CLI rejects cross-mode actions and unsigned IPA before external reservation. API fixtures use real TLS, ES256 verification, byte ranges and durable SQLite, with a substituted signed-package dependency.
- 70 official Apple documentation JSON resources cached with SHA256/source URLs. npm CLI audit could not complete (quick endpoint HTTP400); direct official bulk advisory lookup for 126 locked package names returned zero advisory packages. No dependencies added.
- Preview original resources, identities, credentials and bootstrap preserved across restart; original password login verified, adding one normal session. Test PostgreSQL stopped and retained.
- Source/config format, local links and diff whitespace checked. No commit, push, real Apple mutation or full-product completion claimed.
