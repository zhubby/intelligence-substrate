# Delivery Cost Accounting Implementation Plan

> Execute sequentially using superpowers:executing-plans; the user's main-thread-only AGENTS instructions and already approved product plan apply. Use writing-plans to maintain this task plan; no subagents, commits or paid/provider calls.

**Goal:** Make delivery cost records traceable and aggregate them without treating absent or uncertain amounts as actual zero cost.

**Architecture:** Add expense resources and an authorized, transactional cost report over existing charges and expenses. Reuse command auditing/idempotency and backup storage; keep the model-budget admission rules unchanged.

**Tech Stack:** Go, SQLite/PostgreSQL, React/TypeScript, Playwright.

**Spec:** ../specs/2026-11-06-delivery-costs-design.md

## Global Constraints

- USD integer cents 0–100000000; explicit source, note, timestamp and retry key.
- Human owner writes; current project access reads. Immutable amounts, auditable voids, active reference uniqueness across the project.
- Unknown cost is visible; no payment or total-cost-completeness claim. Existing independent acceptance and approvals remain authoritative.
- All shell/subprocess commands start with rtk. Use the task root referenced by work/current-delivery-costs-root.txt.
- Preserve original preview/static bytes, staging, previous source outside this increment, all155 outputs and original Docker resources; no cleanup, commit, push or external-account actions.

## Task 1: Durable expense ledger

Files: new internal/platform/expenses.go, delivery_costs_test.go and delivery_costs_postgres_test.go; update service.go and engineering.go.

- [x] Write command tests first for record/replay/conflict, same-source duplicates across actors/goals, void/correction trace, amount/source/time validation, proof scope and unauthorized writes. Observe unknown-command failure.
- [x] Implement human-owner record/void in Store.Update with Once/Remember and existing audit. Snapshot requirement and proof binding; retain original records after void/revision.
- [x] Add a red regression for fractional/non-numeric model reconciliation, then enforce integer cents with the shared strict validator.
- [x] Exercise two separate PostgreSQL clients contending for one source and archive/restore the resulting ledger plus nonempty idempotency.

## Task 2: Authorized cost report API

Files: extend delivery_costs.go; update internal/httpapi/server.go; add internal/httpapi/delivery_costs_test.go.

- [x] Write actual HTTP report tests showing the route missing; seed synthetic charge states with hand-derived expected subtotals and ensure no use of goal.spentCents as a second charge source.
- [x] Read current permissions and all selected resources in one transaction, exposing estimate/reconciled/pending/external subtotals, records, missing categories and unmeasurable entries.
- [x] Test goal/project isolation, current revocation, anonymous/CSRF/Agent write rejection, unknown/fractional historical data, voids and unchanged budgets/release/quality state.

## Task 3: Workbench and delivery evidence

Files: new web/src/DeliveryCosts.tsx and web/tests/delivery-costs.spec.ts; update Operations.tsx, styles.css, api.ts as needed; document docs/delivery-costs.md, API/validation/README and original plan ledger.

- [x] Write a failing browser workflow using the real HTTP/SQL/auth path and synthetic external-cost provenance.
- [x] Reuse existing forms, cards and model reconciliation; support target selection, known/pending/coverage distinctions, first recording, duplicate prevention, void history and corrected source linkage.
- [x] Run TypeScript/Vite into an isolated outDir and Chrome 1440x1000/390x844; inspect screenshots and verify keyboard/focus/overflow and read-only access.
- [x] Run appropriate full Go race/vet, isolated PostgreSQL, existing workbench regression and formatting/link checks. Review sequentially and retain red/green evidence.
- [x] Verify preservation, export current source/report/runbook/evidence/screenshots into outputs and independently read back all hashes. Complete only todo009; retain the full product goal/P3 as active.

## Verification ledger

- Full Go race: 353 top-level / 1334 including subtests, no failure/test skip; dedicated PostgreSQL17.6 contention and archive/restore include3 nonempty replay rows. go vet, build and module tidy diff passed.
- Final Chrome flows:2 cost workflows plus8 existing workbench/membership workflows, desktop1440x1000/mobile390x844, no failure/skip/flaky retry. Actual HTTP/SQL/auth; synthetic expense records and task-owned local503 model stub only.
- Final TypeScript/Vite build, gofmt/Prettier, whitespace and82 local links passed. Reviewed source/transaction/authority/accounting/UI sequentially; no independent code reviewer. Final HTTP token test wording was corrected and rerun with race; owner-role non-human rejection is proven separately in the platform test.
- Red evidence and initial harness failures remain private. Test corrections fetch the stored UpdatedAt snapshot and expect the existing gateway502 for provider503; neither change weakens production behavior.
- Initial preservation:368 original +11 new source files,155 original outputs,261 staged entries,0 commits, original18080/PID36063/static bytes and32 containers/23 image references/15 volumes retained. Task servers/PostgreSQL/stubs stopped.
- Apple SDK probes at06:26UTC still exit69. No agreement acceptance, native build success, actual invoice/payment, external notification or production/store operation. P3 remains open.
- Six new public files exported and independently read back:379 source hashes match the workspace and archive,155 prior output hashes unchanged; PNG sizes1440x1667/390x2110. Only todo009 is complete. Completion records are synchronized to the final archive with a second guarded export/readback of this increment's own files.
