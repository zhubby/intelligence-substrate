# Design and Prototype Baseline Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans task-by-task, sequentially in the main thread. User instructions prohibit delegation and Git publication.

**Goal:** Carry versioned design decisions and real prototype artifacts through human plan approval into exact Agent execution context.

**Architecture:** Keep immutable `design` proposals in the existing resource store. Bind a canonical snapshot into the plan and its approval; select approved context from that frozen approval. Reuse the existing command, lease, invalidation, model intent and preview boundaries.

**Tech Stack:** Go, existing SQLite/PostgreSQL transactions, React/TypeScript, Playwright and standard-library evidence scripts.

**Spec:** `docs/superpowers/specs/2026-11-06-design-baseline-design.md`

## Global Constraints

- Sequential main thread; every shell command uses `rtk`.
- No staging, commits, pushes, original-preview deployment, paid models, real store or production actions.
- Do not accept Xcode agreements or change global Xcode selection.
- Preserve 498 existing source files, 233 older outputs, 261 index entries, zero commits, original preview/Docker/AVD.
- Scratch, logs and private fixtures stay in ignored `work/`; public exports use the user's existing outputs directory.
- Full P3 remains open.

## Task 1: Durable proposals and frozen plan approval

**Files:** Create `internal/platform/designs.go`, `designs_test.go`, `designs_postgres_test.go`; modify `service.go`, `goals.go`, `releases.go`, `plan_generation.go`.

**Interfaces:** `designs.propose` accepts goalId, expectedRequirementRevision, key, title, summary, decisions, criteria, prototypeArtifactIds, reason and optional worker runId/leaseToken. `plans.propose` and generation accept optional designId/expectedDesignBinding. `ApprovedDesign(goal Resource, planID string, resources []Resource) (M, error)` returns a validated frozen snapshot or nil for legacy plans.

- [x] Write proposal/approval tests using a real Git commit and actual uploaded HTML, then run `rtk proxy go test ./internal/platform -run Design -count=1`; first failure must be unknown command or missing selection behavior.
- [x] Implement strict fields, bounded typed arrays, immutable content binding, current scope/lease, exact prototype validation and idempotency. Test foreign, stale, corrupt and forged inputs without relaxing existing signing/quality permissions.
- [x] Bind explicit or inherited design into proposed plan; freeze it in the human approval. Recheck actual bytes before task creation. Test replacement cancels old work and stales release candidates; changed requirement marks design stale.
- [x] Add PostgreSQL two-client same-key proposal test, atomic plan approval and backup/restore replay. Use a task-owned local PostgreSQL process and schema, preserving existing Docker.

## Task 2: Model planning and actual Worker consumption

**Files:** Modify `internal/platform/plan_generation.go`, `internal/integrations/plans.go`, `internal/worker/agent.go`, `internal/httpapi/mcp.go`; create focused design tests in integrations/worker/httpapi and a focused worker design-result file as needed.

**Interfaces:** Model generation pins the user's selected design in persisted request input; model output remains summary/risk/tasks. Worker finish may include structured design with optional prototypePath/target; the controller supplies prototypeArtifactIds and lease metadata. Worker consumes `approvedDesign` from the approval selector.

- [x] Write failing tests for pinned model input, output selection substitution denial and selected-record drift. Use only a deterministic local HTTP provider; verify the provider's actual received request and the persisted proposal.
- [x] Implement generation selection/context validation and API/MCP parity. Preserve costs and exactly-once intent behavior.
- [x] Write failing Worker tests that prove exact frozen decisions reach the actual model callback and a malformed approval prevents it. Publish a real HTML file from a leased design task; verify stored artifact bytes, design record, lack of approval and refusal for non-design/expired/foreign leases.
- [x] Implement the minimal Worker finish path and read context. Run targeted and affected Go package tests.

## Task 3: Delivery workbench and browser acceptance

**Files:** Create `web/src/Designs.tsx`, `web/tests/designs.spec.ts`; modify `web/src/PlanGeneration.tsx`, `forms.tsx`, `pages/Delivery.tsx` and scoped existing styles as needed.

**Interfaces:** Design proposal form captures goal/revision/key. Plan forms choose exact current design ID/binding; cards/confirmation render the plan's frozen snapshot. Prototype links use the existing authenticated preview route.

- [x] Write browser scenarios for proposals, previews, plan selection/finalization, changed baseline, retry idempotency, keyboard/Escape/focus, viewer restrictions and mobile overflow. Run once before UI changes and record the missing-control failure.
- [x] Implement the existing-page extension using shared form/dialog components. No unrelated redesign or original-preview rebuild.
- [x] Run isolated desktop/mobile design, planning and existing affected workbench suites through `scripts/verify-workbench.py`; inspect screenshots and server state.

## Task 4: Regression, review and reviewable delivery

**Files:** Create `docs/design-baselines.md`; update README, API and validation references, this plan and canonical todo 020. Verification/export scripts remain in ignored `work/`.

- [x] Run complete Go race suite with actual PostgreSQL, vet/build, TypeScript, isolated Vite build and appropriate browser suites. Review security and simplicity sequentially and correct evidenced issues with regression tests.
- [x] Independently read SQL/backup/artifact/approval evidence, compare the fresh preservation baseline, scan public payloads for private bytes and export source/report/runbook/evidence using a unique design-baseline prefix. Do not export raw test credentials or private database files.
- [x] Read back exports independently, finish only todo 020, and report exact verified behavior and remaining full-product gaps. Keep the overall goal active.

## Final verification notes

- Security review reproduced standalone prototype self-navigation against the actual isolated service and Chrome. A browser regression first observed one forbidden request; escaped sandboxed srcdoc plus an outer frame-src policy prevents it while retaining internal interaction. This is browser isolation, not an enterprise network firewall.
- Final post-fix Go race: 478 top-level / 1,733 including subtests; actual PostgreSQL, no test skips or failures. vet/build/tidy and formatting pass.
- Final workbench: 16 files / 32 isolated groups / 58 flows; zero failures/skips/retries, 16 deterministic local provider calls. Desktop/mobile screenshots reviewed; no paid provider or original-preview update.
- Independent Python readback compares 11 restored resource rows and 23 archive files, verifies Git/prototype/plan/approval digests and durable proposal idempotency; no credentials restored. Apple SDK probes still exit 69.
