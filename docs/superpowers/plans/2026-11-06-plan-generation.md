# Durable Plan Generation Implementation Plan

> For agentic workers: use superpowers:executing-plans sequentially in the main thread. No delegation, staging, commits or push.

**Goal:** Generate a governed, replay-safe, context-bound delivery proposal from an approved requirement baseline.

**Architecture:** The SQL control plane persists a plan-generation intent before inference and atomically commits validated proposals with audit/provenance. The existing gateway owns model policy/budget/cost. The workbench and Agent HTTP callers use explicit revision/key.

**Tech Stack:** Go, SQLite/PostgreSQL, React/TypeScript, isolated Chrome/Playwright.

**Spec:** ../specs/2026-11-06-plan-generation-design.md

## Global Constraints

Preserve18080/PID36063/backend/web/dist,261 staged entries/zero commits,173 existing outputs and37 containers/23 image references/15 volumes. No paid models, Apple agreement acceptance, external notification, store or production action. Main thread only. Overall P3 remains open.

## Task1: Durable planning intent and snapshot

Files: internal/platform/plan_generation.go, plan_generation_test.go, plan_generation_postgres_test.go; internal/integrations/model_context.go.

- [x] Add failing tests for current baseline, explicit revision/key, same-key replay, altered payload, protected context changes and manual/Agent authority.
- [x] Run `rtk proxy go test ./internal/platform -run PlanGeneration -count=1`; retain the missing-lifecycle failure.
- [x] Implement `BeginPlanGeneration(context.Context,Actor,M)(Resource,bool,error)`, `ValidatePlanGenerationContext(*Tx,Resource,Resource) error`, `CompletePlanGeneration(context.Context,Actor,string,M)(Resource,error)` and `FailPlanGeneration(context.Context,string,string) error`.
- [x] Build canonical input transactionally; reuse existing proposal validation, bind generation/charge/input and audit atomically. Reject unsafely injected response fields.
- [x] Verify two PostgreSQL clients compete for one intent and archive/restore preserves succeeded/running states, provenance and idempotency rows.

## Task2: Governed integration and HTTP callers

Files: internal/integrations/plans.go, plans_test.go, model.go; internal/httpapi/server.go, plan_generation_test.go; existing integration tests and scripts/verify-live-model.go.

- [x] Add failing integration tests that replay one key after success/failure/in-flight without another provider attempt, and reject changed context or authority while retaining cost.
- [x] Change `GeneratePlan(context.Context,Actor,M)(Resource,error)` to use persisted input. Require settled completed charge before the existing plan validation.
- [x] Teach the private gateway intent path to recognize plan_generation with its own roles/approved context and planRequestId; ordinary Chat fields still cannot choose it. Reject repeat charge reservation.
- [x] Forward the full strict request at HTTP; update source callers to include current revision and an explicit new key. Keep lifecycle methods absent from commands/MCP.
- [x] Verify policy/budget/context/format/truncation/audit and actual Agent token/CSRF boundaries with local HTTP providers.

## Task3: Workbench confirmation and records

Files: web/src/PlanGeneration.tsx, web/src/pages/Delivery.tsx, web/tests/plan-generation.spec.ts, scripts/verify-workbench.py, web/playwright.config.ts.

- [x] Add a browser test expecting a confirmation before a request and a persistent record; run the focused desktop suite and retain the missing-confirmation failure.
- [x] Capture key/goal/revision for the dialog lifetime. Refresh on success/failure. Render request history and proposal provenance, and preserve independent approval.
- [x] Extend the local fixture runner with deterministic successful/failed planner replies. Verify same-key replay, failure-to-manual path, permissions, goal switching, Escape/focus and mobile overflow at390px.

## Task4: Verification and delivery

- [x] Run `rtk proxy go test -race -count=1 -json ./...` with a dedicated PostgreSQL URL; inspect all test events and stop the cluster.
- [x] Run vet/build/tidy, types/isolated Vite, full standard browser runner and format/whitespace/link checks.
- [x] Kill only an owned test platform after its provider receives a request; restart the same data/binary, verify replay409 and one provider call with running/reserved state.
- [x] Review source increment in the main thread, verify hashes of all protected source/output/preview/staging/Docker state and stop task services.
- [x] Update API/operator/validation docs; export source/report/runbook/evidence/screenshots and independently verify every artifact hash/source entry. Close only todo012 and this plan after readback.

## Scoped completion

The 2026-11-06 08:37UTC independent readback verified all6 deliverables,419 source files against the ZIP manifest/current bytes,403 top-level Go tests/1472 with subtests and42 browser flows. All test services are stopped, including the focused PostgreSQL port64033. The111 local documentation links and protected preview/staging/output/Docker inventories pass. Only this implementation increment and todo012 are complete. Overall P3 remains open; both Apple SDK probes still returned license69 at08:36UTC. The completion-only todo rename and plan update are exported again and independently rechecked before reporting delivery.
