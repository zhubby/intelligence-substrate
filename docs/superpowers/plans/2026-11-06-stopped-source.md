# Stopped Agent Source Preservation Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially. User AGENTS.md prohibits subagents, commits and pushes.

**Goal:** Preserve actual code output when an Agent returns before finish, then use the existing human source-recovery and independent quality gates.

**Architecture:** The trusted Worker fingerprints its original source and records a bounded private tar on abnormal code exit. It attempts the existing authenticated snapshot protocol only under the original context and leaves source adoption to the existing human-only reconciliation command. No model/session replay or new authority protocol.

**Tech Stack:** Go, existing source tar writer, native Git, authenticated HTTP, SQLite/PostgreSQL, existing React source-recovery UI.

**Spec:** `docs/superpowers/specs/2026-11-06-stopped-source-design.md`.

## Global constraints

- All shell commands use RTK; implementation and review stay in the main thread.
- Preserve the original preview PID 36063 on port 18080, existing credentials, SDKs, databases and all 97 previous output files.
- No commit/push, paid model rerun, signing or store publication for this increment.
- Original errors, nonzero exit, fees, budgets and retry ceilings remain authoritative. Source preservation never grants quality/release success.
- Local state contains no lease, platform/model credentials or model conversation. Source selection retains existing limits and path/mode rules.

## Task 1: Reproduce the measured loss through real budget enforcement

**Files:** Create `internal/worker/stopped_source_test.go`; use real `internal/integrations.Model`, HTTP handler, Worker and Git.

**Interfaces:** Existing `RunOnce`, `InspectSourceOperation` and `sources.reconcile`. No new production interface needed for the initial regression.

- [x] Write an HTTP provider fixture returning one measured write action. Set task budget so the first call is admitted and the next is rejected by the actual model gateway. Assert a durable source operation exists while the original run is blocked/nonzero, no evidence exists, and worker adoption is rejected.
- [x] Run `rtk proxy go test ./internal/worker -run TestStoppedAgentSource -count=1 -v`, preserve the expected missing-source failure under a new ignored evidence root.

## Task 2: Preserve accurate private source and register an incomplete candidate

**Files:** Create `internal/worker/stopped_source.go`; modify `internal/worker/agent.go`, `internal/worker/source.go`, `internal/worker/runner.go`; add boundary tests to `stopped_source_test.go`.

**Interfaces:** Add an original source fingerprint and submission-attempt flag to `sourceWorkspace`; compute fingerprint before the Agent. Named Agent return values allow a defer to attach preservation metadata without replacing the original failure. Reuse `commitSource` and existing server-side lease/binding checks.

```go
// snapshotSource(ctx, state, root, writer) selects the same regular source files
// as a normal commit and returns the SHA256 of the canonical tar bytes.
// preserveStoppedSource writes a private fsynced archive/metadata pair before
// attempting any upload; result contains operationId only after a confirmed ack.
```

- [x] Implement canonical source fingerprinting with the existing writer and store the baseline before model actions. Use a bounded context for local preservation after cancellation.
- [x] On abnormal code return, compare actual source and durably preserve changed bytes in a new private directory under Worker state. Exclude credentials, conversation and control metadata. Keep original error and exit unchanged.
- [x] Attempt one existing bounded idempotent snapshot upload only if original context is active and final submission has not started. Add confirmed candidate ID or explicit upload failure to the original result; local archive stays retained.
- [x] Verify the initial regression and boundary cases: no change/non-code, binary/delete/mode, cancelled/invalid lease, upload uncertainty, unsafe snapshot and a prior final-commit attempt. Confirm no duplicate snapshot identity or false completion.

## Task 3: Verify human adoption, build and independent gates

**Files:** Extend `stopped_source_test.go` and actual CLI/HTTP acceptance under ignored `work/`; update `docs/source-recovery.md`, `docs/workers.md`, `docs/validation.md`, README and this plan.

**Interfaces:** Existing read-only source inspection and human `sources.reconcile`. Downstream task binding consumes the original candidate only after confirmed recovery.

- [x] Adopt the exact stopped candidate as the synthetic local owner through the existing command. Verify original producer/run/error/exit history, actual build output, independent QA and browser-gate rejection.
- [x] Exercise the existing source-recovery workbench against the stopped-candidate shape at desktop/mobile widths if UI behavior changes or a new concern appears. Do not claim model session, physical device or external channel recovery.
- [x] Run appropriate focused regressions, then full vet/race with a new dedicated PostgreSQL cluster and three-platform program compilation. Stop only this increment's temporary resources.
- [x] Main-thread correctness/security/simplicity review; export new source/report/evidence with exact-byte verification, retain all original 97 digests and preview. Keep full product/P3 goal active.
