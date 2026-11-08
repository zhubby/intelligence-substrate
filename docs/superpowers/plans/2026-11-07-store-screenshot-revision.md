# Store Screenshot Revision Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in the main thread. User instructions prohibit delegation and Git index/commit changes.

**Goal:** Correct approved App Store screenshots on the original rejected build, wait durably for processing, then resubmit and require exact independent publication acceptance.

**Architecture:** Extend the existing independent material approval with an explicit screenshot scope. Keep original text hashes compatible, reuse publisher authority/journaling and channel polling, and add a separate image-processing/placement continuation before the existing final resubmission.

**Tech Stack:** Go, SQLite/PostgreSQL, HTTP Worker, existing React/Playwright.

**Spec:** `docs/superpowers/specs/2026-11-07-store-screenshot-revision-design.md`

## Global Constraints

- Main thread only; no stage/commit/push, paid models, real stores/production or Apple license acceptance.
- Preserve original package/approval/build/review item and other targets; only exact new material approval authorizes screenshot changes. Old text-only records remain valid and cannot change media.
- No image-library deletion or blind retries. Persist write intent, retain app/version/successor ownership, and stop on unclear outcomes.
- Baseline764 sources/343 outputs/261 staging entries/zero commits; original preview/Docker/AVD/keychain preserved. Overall product/P3 remains open.

### Task 1: Scope and controlled continuation

**Files:** `internal/platform/release_issues.go`, `store_revisions.go`, `store_revision_tasks.go`, `tasks.go`, `releases.go`; `internal/worker/runner.go`; `internal/publisher/publisher.go`.

- [x] Add failing control-plane screenshot-scope and processing/continuation tests; retain red.
- [x] Bind screenshot scope to triage/spec/approval/task/current target/query/Worker request; preserve absent-field text compatibility. Enforce kind/state/scope and current authority.
- [x] Verify independent approval, no package/source change, processing releases lease, exact continuation, stale scope/decision rejection, repeat completion and publication binding.

### Task 2: Publisher screenshots and durable waiting

**Files:** `internal/publisher/apple_material_revision.go`, `apple_material_upload.go`, `apple_material_api.go`, `apple_material_revision_journal.go`; add focused screenshot helpers/tests.

- [x] Confirm current official delete/create/order contracts and retain originals; reproduce media correction failure with real PNG/TLS fixtures before implementation.
- [x] Stage exact approved material; reuse accurate images, upload changed images with journal checkpoints, and persist complete processing results without deleting old placements.
- [x] Add read-only processing/status and single-winner continuation CAS, then delete exact old placements, create/order/read back new placements and reuse original resolve/resubmit. Preserve shared images and original receipts.
- [x] Test two rounds, reorder/count/bytes, text-only denial, preflight and late drift, two Engine continuation, pending/unknown no-retry, lost accepted final response/read-only reconciliation and exact final journal bytes.

### Task 3: Product, persistence and verified delivery

**Files:** `web/src/ReleaseIssues.tsx`, `StoreRevisions.tsx`, affected browser suite; new PostgreSQL/Worker tests; docs/runbook, validation and main ledger.

- [x] Prove scope survives actual HTTP Worker transport, no IPA download on correction/status/continue, and PostgreSQL concurrency/replay/archive restore.
- [x] Add screenshot-specific desktop/mobile browser assertions; observe red before UI; show independent approval, processing, readiness, waiting review and accurate publication verification.
- [x] Run full Go race with isolated PostgreSQL, vet/build/tidy, Python and affected browser/frontend checks; inspect final images and review changed code sequentially.
- [x] Verify all baseline resources and previous outputs; export report/runbook/source/evidence, independently read back, close only042, re-export bookkeeping and read back final deliverables. Keep overall goal active.
