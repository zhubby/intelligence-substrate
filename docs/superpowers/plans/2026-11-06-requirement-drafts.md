# Requirement Drafts Implementation Plan

> For agentic workers: execute sequentially with superpowers:executing-plans. User instructions require the main thread only.

**Goal:** Turn a business description and confirmed clarification into a human-reviewed, separately approved requirements baseline.

**Architecture:** Existing transactional resource, audit and idempotency storage owns proposal/application state. Private model intents share the governed gateway. React uses the same versioned commands.

**Tech Stack:** Go, SQLite/PostgreSQL, React/TypeScript, isolated Chrome/Playwright.

**Spec:** ../specs/2026-11-06-requirement-drafts-design.md

## Global Constraints

Preserve18080/PID36063, web/dist,261 staged entries,167 outputs and Docker. No staging/commits/push, paid models, Apple acceptance, external notification or actual channel/production action. P3 stays open.

## Task 1: Raw goals and reviewed proposals

Files: internal/platform/requirement_drafts.go, goals.go, service.go, requirement_drafts_test.go.

- [x] Add command-level failing tests: empty baseline cannot approve; propose/apply retains exact human answers; changed context rejects; repeat does not create another version; nonhuman apply denied.
- [x] Run `rtk proxy go test ./internal/platform -run RequirementDraft -count=1`, retain red evidence.
- [x] Implement strict content/snapshot validation, input digest and human application through existing revision/invalidation path. Add malformed input, audit rollback and current-authority cases.
- [x] Verify SQLite and dedicated PostgreSQL two-client contention/archive/restore.

## Task 2: Durable governed generation

Files: internal/platform/requirement_generation.go, internal/integrations/requirements.go, model.go; HTTP and cmd/platform wiring, MCP proposal exposure; focused tests.

- [x] Add failure/replay, invalid output, policy/budget, forged public mode, revocation and late-context tests before implementation.
- [x] Save intent before network; admit only known internal purpose and exact captured context, link charge; atomically complete proposal/audit or retain failed intent and cost.
- [x] Verify real HTTP auth/CSRF, Agent proposal and blocked human/internal commands, actual planner reads adopted brief and frozen decisions.

## Task 3: Reviewed requirement workflow

Files: web/src/RequirementDrafts.tsx, forms.tsx, pages/Delivery.tsx, api.ts, styles.css; web/tests/requirement-drafts.spec.ts; scripts/verify-workbench.py.

- [x] Add browser flow for raw business description, question/answer, budget-confirmed generation, edited adoption and separate approval. Capture expected missing-UI failure.
- [x] Implement responsive review/edit forms, provenance/history, errors, disabled gates, stable per-dialog keys and captured goal/revision.
- [x] Add deterministic local provider fixture; verify full standard npm browser suite without touching original preview.

## Task 4: Verification and delivery

- [x] Run full Go race with dedicated PostgreSQL, vet/build/tidy, frontend formatting/types/build, browser suite; inspect results and screenshots.
- [x] Review security/correctness sequentially in the main thread; compare source baseline and all protected state.
- [x] Update documentation and exact validation ledger, export source/report/runbook/evidence/screenshots, independently read hashes and archived source; close only this increment.

## Verified increment completion

All four scoped tasks passed local verification: Go race389 top-level/1428 including subtests, dedicated PostgreSQL concurrent adoption/actual restore,13 browser files/26 isolated groups/38 flows, actual SIGKILL/restart replay protection, static/build checks and independent output readback. The original preview,261 staged entries,167 prior outputs and the current37-container baseline remain intact. Only this increment is complete; the full P3 product acceptance remains open. No commit, push, paid model, Apple agreement, store or production action occurred.
