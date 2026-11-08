# Accepted Screenshot Commit Recovery Implementation Plan

> **For agentic workers:** Execute sequentially with superpowers:executing-plans. User instructions prohibit subagents, staging and commits.

**Goal:** Recover positively accepted screenshot revisions into durable processing waits without replaying external writes or bypassing protected continuation.

**Architecture:** Extend the existing administrator-only ReconcileApple path with a separate screenshot-processing observation. Reuse exact artifact/material checks and the existing atomic completion transaction, adding a source-parent CAS. Keep Worker protocols, control-plane permissions and UI unchanged.

**Tech Stack:** Go, SQLite, existing App Store TLS/ES256 provider contracts.

**Spec:** ../specs/2026-11-07-apple-media-recovery-design.md

## Global constraints

- Preserve873 initial sources,383 old outputs,261 Git index entries/flags, zero staged changes/commits, original18080 preview/dist and Docker/AVD/keychains.
- No paid models, real Apple/store/production actions, license acceptance or device authorization.
- Keep unsupported intermediate writes pending; only positive complete-image proof can become a processing wait. No recovered result is a publication success.

## Task1: Exact accepted-commit observation and atomic recovery

**Files:** create `internal/publisher/apple_screenshot_recovery.go` and `internal/publisher/apple_screenshot_recovery_test.go`; modify `internal/publisher/apple_recovery.go` and the existing unknown-write test in `apple_screenshot_revision_test.go`.

**Interfaces:** existing `ReconcileApple(ctx context.Context, in AppleReconciliationRequest) (Operation, error)`; helper `appleScreenshotRecovery(ctx context.Context, p Profile, r Request, receipt *appleReceipt) (Result, operationJournal, error)` returns a non-continuing processing result and the exact parent journal used for final CAS.

- [x] Write a real TLS/SQLite lost-final-commit test that expects processing, old placements and retained reservations, then restart and verify idempotent recovery and ordinary continuation. Capture its failure before product changes.

```go
recovered, err := engine.ReconcileApple(ctx, exactInput)
if err != nil || recovered.State != "complete" || recovered.Result.State != "processing" || recovered.Result.ContinueRequired {
    t.Fatal(recovered, err)
}
```

- [x] Add literal negative scenarios: incomplete second image, uncommitted bytes, changed approved PNG, text/placement/image/catalog drift, changed parent, lost locks, wrong journal, missing stop confirmation, SQL failure and concurrent CAS. Missing IDs are never filled by provider search.
- [x] Implement the helper and route only supported screenshot stages through existing artifact/processed-build verification. Normalize only server IDs when comparing the material manifest. Use current parent and reservation predicates in the final transaction; retain the app lock.
- [x] Change the legacy unknown-commit test to use a partially committed two-image revision so its unsafe-recovery contract stays meaningful. Run all publisher/CLI race tests, vet/build and applicable cross-compilation without modifying unrelated platform/frontend code.

## Task2: Independent recovery evidence and operator delivery

**Files:** update `docs/app-store.md`, `docs/store-screenshot-revision.md`, `docs/store-material-remediation.md`, `docs/product-acceptance.md`, `docs/validation.md`, README and master implementation ledger. Keep temporary proof readers/runners in work/ and deliverables under the existing outputs directory.

- [x] Retain raw pending/recovered/continued SQLite states and API write counters from actual TLS executions, including negatives and concurrency. Independently read journal/source/material/image/provenance/CAS and unchanged write counts without importing product code.
- [x] Document exact supported stages, stopped-process precondition, processing-versus-submitted behavior and remaining partial/media uncertainty. Recheck Apple SDK read-only, preserve environment/index/all383 old outputs, and export source/report/runbook/public evidence without private state or hashes.
- [x] Independently read back final source ZIP bytes/sizes/modes, document links, reports and preservation; close only052 and include completion records in the final export. Leave complete product/P3 active.

## Work log

- 2026-11-07: Captured expected red failures before implementation, then passed all affected publisher/CLI race tests (163 top-level/982 including subtests), vet/native/Linux/Windows builds and five new proof tests (25 including subtests). Independent SQLite/image/journal readback checked27 raw snapshots,18 negative pending cases and two complete recovery/continuation flows. Eighteen actual CLI replay/negative processes attempted zero API contacts; first remote recovery was exercised by Engine against local TLS, not by the CLI or real Apple.
- 2026-11-07: Initial output readback passed879-source/880-entry ZIP byte/size/mode checks,184 source-document links, report/runbook links/end markers and preservation of383 historical outputs,261 index entries/flags, zero commits, original18080/dist and Docker/AVD/keychains. Only052 local recovery implementation is accepted. Final export/readback includes these completion records. Three Apple SDKs still return license69; native/device/enterprise signing/stores/production and complete product/P3 remain open.
