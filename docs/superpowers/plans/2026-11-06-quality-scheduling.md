# Protected Quality Scheduling Implementation Plan

> For agentic workers: use superpowers:executing-plans sequentially in the main thread. No subagents, staging, commits or push.

**Goal:** Automatically schedule the first independent verification of successful build artifacts under a human-bound protected policy, retaining the existing repair and release gates.

**Architecture:** Goal/plan-bound quality_policy rows and per-policy/artifact quality_dispatch records share the SQL transaction with Worker claims. Claims, completions and release gates recheck immutable bindings. The workbench uses the same governed commands.

**Tech Stack:** Go, SQLite/PostgreSQL, existing Git/artifact storage and Worker protocol, React/TypeScript, isolated Chrome/Playwright.

**Spec:** ../specs/2026-11-06-quality-scheduling-design.md

## Global Constraints

Preserve18080/PID36063/backend/web/dist,261 staged entries/zero commits,179 existing outputs and37 containers/23 image references/15 volumes. No paid models, Apple agreement acceptance, external notifications, store or production action. Main thread only. Overall P3 remains open.

## Task1: Human-bound policy and durable automatic scheduling

Files: internal/platform/quality_policy.go, quality_scheduling.go, quality_scheduling_test.go; service.go, tasks.go, adaptive.go and goals.go.

Interfaces:

```go
func (s *Service) qualityCommand(t *Tx, a Actor, command string, in M) (Resource, error)
func (s *Service) scheduleQuality(t *Tx, a Actor, worker Resource) error
func (s *Service) validateQualityTask(t *Tx, task Resource) error
```

- [x] Add TestQualitySchedulingBuildToIndependentEvidence using a real repository, current approved build plan and uploaded lease-bound artifact. Call quality.bind, complete build, claim on a trusted QA worker, complete verification, and assert one dispatch/task/evidence after repeated claims.
- [x] Run `rtk proxy go test ./internal/platform -run QualityScheduling -count=1`; retain the unknown-command failure.
- [x] Implement strict quality.bind/revoke authority, expected version/plan, criteria/method/target/budget/pipeline snapshots, explicit-key replay and limits. Route only these public commands through Service.Command; keep them absent from Agent MCP.
- [x] Schedule from actual successful build run/task/artifact provenance in the trusted QA claim transaction. Use Once/Remember per policy/artifact; persist blocked reasons, with no external action in scheduling.
- [x] Recheck policy at claim/completion; protect quality-generated task cancel/retry; mark policies stale on baseline/plan changes and reserve policy/dispatch fields from external task recipes.
- [x] Verify real permission, cross-project, malformed input, failed/fake build, duplicate/concurrent claim, pipeline drift, revocation and changed baseline behavior.

## Task2: Repair reuse and policy-bound release approval

Files: internal/platform/quality_release.go, defects.go, releases.go, quality_scheduling.go/test.go.

Interfaces:

```go
func (s *Service) qualityPolicySnapshot(t *Tx, goal Resource) ([]any, error)
func (s *Service) releaseQualityGate(t *Tx, release, goal Resource, evidence []Resource) error
```

- [x] Add failing tests that reject a candidate without exact policy evidence and reject old approval after a policy change; prove ordinary pipeline evidence cannot impersonate the bound policy.
- [x] Include current policy IDs/digests in candidate binding and enforce exact artifact/commit/policy evidence in the release gate. Preserve existing no-policy/manual behavior and physical environment requirements.
- [x] Add the failing repair-rebuild test: a new artifact gets exactly one original-policy recheck and one new dispatch, retaining defect closure; other active policies schedule independently.
- [x] Strip old dispatch IDs during repair copying and reuse the existing recheck when scheduling the repaired build. Run focused platform tests and retain red/green logs.

## Task3: Workbench and actual API/Worker acceptance

Files: web/src/QualityScheduling.tsx, web/src/pages/Engineering.tsx, web/src/pages/Delivery.tsx, web/tests/quality-scheduling.spec.ts, scripts/verify-workbench.py; internal/httpapi/quality_scheduling_test.go; internal/worker/quality_scheduling_test.go. The source runner discovers the new suite without changing the default Playwright config.

Review additions: protected dependency rejection with valid ordinary reordering; current Worker trust/runtime/test authority checked before all completion outcomes; actual Worker same-revision strategy-content mismatch blocked before tool execution. Final new lists reuse existing wrapping member-row layout.

- [x] Add a browser flow expecting the automatic-policy form and queue, run against the old UI and retain its missing-control failure.
- [x] Reuse existing Form/Confirm/resource-row controls. Capture dialog goal/revision/plan/key, show policy and dispatch state, and keep human manual verification available.
- [x] Verify HTTP session/CSRF and real Agent/Worker-token denials, target switching, revoked-policy visibility, keyboard/Escape/focus and390px overflow.
- [x] Run actual Worker protected commands against real source/package bytes: build success automatically creates QA work, QA failure creates approved repair, repaired package passes original policy without a second duplicate task.

## Task4: Persistence, full regression and delivery

Files: internal/platform/quality_scheduling_postgres_test.go; docs/quality-scheduling.md, api.md, workers.md, architecture.md, validation.md, README.md and main implementation ledger.

- [x] Race two PostgreSQL clients at policy binding/claim, verify one dispatch and one lease, and restore a real archive with nonempty idempotency and exact source/policy/task/evidence rows.
- [x] Run full Go race with dedicated PostgreSQL, vet/build/tidy, TypeScript/isolated Vite, standard source browser runner, format/whitespace and local links. Stop all owned test services.
- [x] Review increment in the main thread and compare protected source/output/staging/preview/Docker snapshots. Recheck Apple SDK prerequisites without accepting agreements.
- [x] Export source/report/runbook/evidence/screenshots, independently read every source/hash and raw verification record, then close only todo013 and this scoped plan. Keep overall P3 open.

## Verification receipts

- Full Go race:418 top-level/1509 including subtests, no failures or test skips, isolated PostgreSQL stopped; vet/build/tidy-diff passed.
- Standard source browser runner:15 files/30 isolated groups/46 flows. After a scoped layout improvement,4 final quality flows passed again; no skipped/flaky/retried flow. Only14 deterministic local-provider calls, none in quality flows.
- Real Worker tools:failed package check, approved repair/rebuild and original-policy recheck;2 tasks/dispatches,1 passing evidence, actual expected package bytes. A changed fetched strategy with the same revision never executes its marker command.
- Process recovery:actual SIGKILL/restart, valid-lease409, real90.21-second expiry, same task/dispatch with2 runs, old completion409 and1 exact evidence; synthetic HTTP completions, separate from actual tool acceptance.
- Evidence root:work/quality-scheduling-jkhup20o. Earlier failures are retained. Initial delivery readback passed for432 source files,6 artifacts and179 prior outputs; completion metadata is re-exported and read back separately. Overall P3 remains open.
