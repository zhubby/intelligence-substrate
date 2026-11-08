# Agent Cumulative Allowance Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans in the main thread, as required by the user. The approved product plan authorizes this bounded implementation; preserve the unborn branch and original staging, without commits or pushes.

**Goal:** Keep ordinary Agent retries and node replacements within the approved task's original money, active time and model-call limits.

**Architecture:** Derive usage from persisted runs and charge reservations inside SQL transactions. Share admission between retry, claim and inference, retain an immutable run admission snapshot, and let the Worker use the remaining call count with a fresh model session.

**Tech Stack:** Existing Go/database/sql/SQLite/PostgreSQL, HTTP Worker and deterministic local model endpoint.

**Spec:** ../specs/2026-11-07-agent-allowance-design.md

## Global constraints

- Only no-command analysis/code/design without blueprint initialization are governed by this increment.
- A task has at most 24 reserved model requests over all runs; reconciliation never refunds a call.
- Original cumulative budget and timeout apply; reserved/uncertain charges require human reconciliation.
- Do not replay model sessions or tools. Preserve existing source, wait, quality, signing and publication authority.
- Main thread; no paid model, external publication, Apple agreement acceptance, staging or commits.

## Task 1: Admission across ordinary retries

Files: new internal/platform/agent_allowance.go and agent_allowance_test.go; modify tasks.go. Consume persisted task/run/charge resources; produce agentTaskAllowance and a run agentAllowance snapshot.

- [x] Write real command tests: after 40 cents and 120 seconds of a 100-cent/600-second task, a replacement run reserves exactly 60 cents and at most 480 seconds. Failed and expired attempts must both satisfy this.
- [x] Observe existing code allocate 100 cents again; preserve the red output.
- [x] Derive cumulative usage and request count, reject unsettled/uncertain expenses and exhausted limits at retry/claim, preserve legacy wait step floors and unused failure attempts on denied claim.
- [x] Verify each refusal leaves no extra run, charge or tool execution, and unrelated tasks remain claimable.

## Task 2: Enforce the same limits immediately before inference

Files: integrations/model.go, new integrations/agent_allowance_test.go, worker/agent.go and new worker/agent_allowance_test.go. Consume ValidateAgentModelAdmission within the existing charge reservation transaction; produce no additional external protocol.

- [x] Write failing HTTP model tests for a task with 24 historical calls, unresolved charge, upward cost reconciliation after claim, replaced plan and expired deadline. Provider requests and charge rows must stay unchanged.
- [x] Gate inference in the same transaction as charge creation, then start the Worker loop from the run's usedModelCalls/messageStepsUsed floor.
- [x] Verify a real Worker on a replacement node consumes only its remaining calls with durable inbox context and no replay of old commands.

## Task 3: Recovery, regression and deliverables

Files: new platform/agent_allowance_postgres_test.go; existing operations documentation and main ledger; work scripts and four new user-facing output artifacts.

- [x] Race two PostgreSQL connections for one bounded replacement lease and one last allowed request; verify portable backup/restore retains charge/run usage and revokes old credentials.
- [x] Read raw SQL/audit/backup independently and verify the fixed local provider's request count; retain failures and final logs separately.
- [x] Run full Go race with isolated PostgreSQL (`rtk proxy go test -race -count=1 -timeout=20m -json ./...`), vet/build/tidy and Linux/Windows Worker compilation. Frontend source is unchanged; original dist is preserved.
- [x] Compare all baseline source/index/outputs/environment records, write report/runbook/source ZIP/evidence under the authorized outputs folder, independently read them back, close only 043 and keep overall P3 open.

## Local increment acceptance

All 043 acceptance steps passed: full Go race727/2579,40 PostgreSQL tests,10 affected browser flows,4 viewed images, actual HTTP Worker and90-second process crash drill, independent database/archive/audit verification and first four-output source/hash readback. Only043 is closed. Final export/readback includes this bookkeeping; fullP3, real-model decisions, native/device/enterprise/store/production and Apple license69 remain open.
