# AX Sandbox Recovery Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans in the main thread as required by the user; no subagents, staging, commits or pushes. This is within the approved enterprise recovery plan.

**Goal:** Persist AX command ownership before external creation and fence replacement work until exact sandbox cleanup is confirmed.

**Architecture:** Transactional sandbox commands own the durable lifecycle. Worker prepares an identity, executes it once and submits exact cleanup observations; existing task/source/model authority consumes unresolved sandbox barriers. Human owner reconciliation requires a disabled original Worker and no active original lease.

**Tech Stack:** Existing Go/database/sql/SQLite/PostgreSQL, pinned AX CLI and local gRPC contract provider, HTTP Worker.

**Spec:** ../specs/2026-11-07-ax-sandbox-recovery-design.md

## Global constraints

- Preserve 780 original source files outside scope,351 previous outputs,261 original staged entries, unborn branch,18080/PID36063/dist, Docker/AVD/keychain baseline.
- No paid model, real store/production action, Apple agreement acceptance, real AX cluster installation or unrelated resource cleanup.
- A list/read failure is not proof of absence. Worker death does not prove remote command termination. No automatic tool replay or session migration.
- Only original Worker finish or authorized human owner reconciliation can close a sandbox; neither revives a lease or adopts a tool result.

## Task 1: Durable lifecycle and admission

Files: internal/platform/sandboxes.go, sandboxes_test.go, sandboxes_postgres_test.go; service.go and tasks.go. Inputs are existing current Actor/run/task and a validated backend/command identity. Outputs are sandbox Resources, exact receipt replay and SandboxRunGate/goal admission.

- [x] Add TestSandboxExpiredRunFencesReplacement proving a persisted prepared sandbox prevents a second run; retain actual red output from the existing implementation.
- [x] Add prepare/finish/reconcile tests for current/expired/revoked roles, exact binding, replay/conflict, disabled original Worker and failed transaction; run with `rtk proxy go test -race ./internal/platform -run TestSandbox`.
- [x] Implement sandbox commands in the existing transaction and gate retry/claim/completion/Worker result authority. Keep active unrelated tasks and goals available; canceled/expired records remain durable.
- [x] Race isolated PostgreSQL clients for one prepare/decision, verify rollback and backup/restore retains the barrier while dropping credentials.

## Task 2: Bound AX command lifecycle

Files: internal/integrations/ax_exec.go, ax_cleanup.go and ax_cleanup_test.go; internal/worker/sandboxes.go, sandbox_test.go, runner.go and agent.go. Inputs are the prepared sandbox ID and fixed AX backend; outputs are actual exit and cleanup result, followed by a durable finish receipt.

- [x] Write cleanup tests that fail when Task deletion fails, list is unknown/denied, or a deleted name is still listed. Preserve the existing silent-success red output before implementation.
- [x] Add RunOwnedCommand and positive absence confirmation using the pinned CLI contract; always try both exact deletions, never delete an unrelated name.
- [x] Route fixed commands and Agent run actions through execRun; prepare must commit before any apply, existing prepares never run again and finish failure never becomes successful output.
- [x] Verify authenticated HTTP Worker with actual child CLI processes for normal/nonzero/partial-create/cleanup-failure and process-death paths; retain raw SQL and provider logs.

## Task 3: Independent validation and handoff

Files: web/src/SandboxRecovery.tsx, web/src/pages/Delivery.tsx, web/tests/sandbox-recovery.spec.ts, docs/ax-sandbox-recovery.md, docs/ax-execution.md, docs/workers.md, docs/api.md, docs/validation.md, main implementation ledger and four output artifacts.

- [x] Add the existing-style goal-level recovery UI across plans; require disabled original Worker, inactive lease, exact identity and explicit owner observation. Verify desktop/mobile browser and screenshots.
- [x] Verify pinned actual AX CLI against a local gRPC provider, including successful absence and retained/failed reads; do not equate it with real Substrate execution.
- [x] Run full isolated-PostgreSQL Go race/vet/build/tidy, Linux/Windows Worker cross-compilation and affected browser flows. Read raw SQLite/TAR/audit/CLI observations independently.
- [x] Compare the original source/index/output/environment snapshots; export report/runbook/source ZIP/evidence and read every exported source byte/size/mode plus log summaries and links independently.
- [x] Close only044 after acceptance; re-export completion bookkeeping and keep fullP3/native/device/enterprise/store/production acceptance open.

## Verification record

Final race: 739/2610, PostgreSQL 41, zero failures/skips. Browser 12, four images viewed. Real Worker/control-plane kills and90-second expiry/reconciliation/replacement pass against local CLI; five pinned actual CLI/local-gRPC contracts and five independent backup/audit readbacks pass. Original invalid harness runs retained; final process/browser binary identical. No actual cluster, enterprise/native/device/channel or fullP3 claim.
