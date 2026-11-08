# Enterprise membership authority implementation plan

> **For agentic workers:** Execute sequentially with superpowers:executing-plans, per user AGENTS instructions. No subagents or Git mutations.

**Goal:** Make project membership changes and offboarding invalidate delegated access atomically, including stale authenticated requests.

**Architecture:** Reuse Store.Update serialization, persistent member resources and credential rows. A transaction-local authority check binds authenticated Actors to current credentials and identity/issuer/Worker state. The existing governance view presents the same scoped API.

**Tech Stack:** Go, SQLite/PostgreSQL, React/TypeScript, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-06-membership-authority-design.md`

## Global constraints

- Preserve 135 outputs, 261 staged entries, 0 commits and PID36063/18080. All new build/test data goes under `work/membership-authority-w2_us9l9`.
- Prefix every shell/subprocess command with rtk. Main thread only.
- No paid models, real account disablement, existing credential changes, production deployment, store writes or Apple license acceptance.
- Preserve human governance decisions, independent QA and unknown external-effect handling.

### Task 1: Atomic membership lifecycle

**Files:** `internal/platform/memberships.go`, `memberships_test.go`, `service.go`, `store.go`; existing `platform_test.go`, `message_inbox_test.go` fixtures.

- [x] Write and run failing tests for missing/disabled identities, strict roles, stale revisions, last-owner demotion/removal, revoke/rejoin, cross-project token preservation, identical-role idempotence and rollback.
- [x] Implement registered identity grants, human-owner revocation, last-active-owner protection, project-specific delegated credential deletion and audit within one transaction.
- [x] Extend global identity disable to revoke memberships atomically without orphaning projects; update synthetic fixtures to register actual human identity records.

### Task 2: Current authority and issuance

**Files:** `internal/platform/authority.go`, `model.go`, `service.go`, `internal/httpapi/auth.go`, `auth_lifecycle_test.go`, `membership_postgres_test.go`.

- [x] Reproduce cached Agent/Worker/human authority after credential revocation, issuer demotion, identity disablement and Worker changes.
- [x] Attach JSON-hidden credential reference; validate current identity/credential/issuer/Worker within governed transactions and authentication. Move session/token insert and audit under the same authority transaction.
- [x] Test HTTP/Basic/MCP/command access, scoped revocation, concurrent issuance versus demotion, concurrent owner removal, audit failure rollback and dedicated PostgreSQL serialization.

### Task 3: Governance UI

**Files:** `web/src/pages/Operations.tsx` (or focused `web/src/Members.tsx`), `web/src/styles.css`, `web/src/api.ts`, `web/src/main.tsx`, `web/tests/memberships.spec.ts`.

- [x] Add role edit/removal with required reason/revision, retained member status and explicit regrant. Reuse current forms/confirmations and local error handling.
- [x] Refresh accessible project selection on membership loss. Verify real API and browser desktop/mobile flows, Escape, stale errors, readable identity/roles and no horizontal overflow.

### Task 4: Full verification and delivery

**Files:** README, `docs/api.md`, `docs/operations.md`, `docs/validation.md`, new membership runbook and todo005. Evidence/scripts stay in ignored work.

- [x] Main-thread correctness/security/contract review; fix findings with meaningful regression evidence.
- [x] Run gofmt/vet/full race using a dedicated PostgreSQL cluster, tidy/diff, 21 builds, isolated Web build and relevant browser tests. Stop test services/cluster.
- [x] Recheck Apple prerequisites without agreement acceptance. Export a new source/report/runbook/evidence and independently read back old outputs, source, staged entries and original preview bytes.
- [x] Complete this increment's tracking only; leave actual native/store/device, full enterprise rollout and P3 acceptance open.

## Completion record

Sequential main-thread implementation and review; no independent reviewer or subagents. Final Go315/1070, authority18/50, dedicated PostgreSQL17.6, 21 builds and browser12 passed. Browser suites run in separate task-owned services per project to respect unchanged production login rate limits; combined-attempt failure remains in evidence. Original preview and135 prior outputs are checked by export and independent readback. Overall product/P3 remains active; no Git delivery, production/store changes or license acceptance.
