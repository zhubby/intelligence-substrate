# Requirement Clarification Implementation Plan

> Execute sequentially using superpowers:executing-plans and TDD. User AGENTS instructions prohibit subagents. The approved product plan authorizes this requirement workflow; preserve staging and do not commit/push.

**Goal:** Connect business questions, human answers and exact baseline approval to planning, with governed model-assisted discovery before implementation starts.

**Architecture:** Version-bound clarification resources share the existing transactional store. Human decisions freeze at approval; a durable generation request guards the model's private draft-only pathway. The workbench and MCP use the same command contracts.

**Tech Stack:** Go/SQLite/PostgreSQL, React/TypeScript, existing model gateway and Playwright.

**Spec:** ../specs/2026-11-06-requirement-clarification-design.md

## Global Constraints

- Only current unapproved requirement revisions accept questions/answers; human product/owner answers; worker denied. Immutable first answers, stable retry keys, current permissions and atomic audit.
- Strict1–10 questions/batch,100/revision; question/context2000bytes, answer12000bytes. Existing scope, budget, independent quality and production gates remain authoritative.
- Private draft model mode only with a persisted current generation intent. Public Chat still requires approval; project policy, budget/interval/uncertain charges apply. Never automatically resend a started generation.
- Main thread, all shell/subprocess commands start withrtk; preserve379 original source files outside this increment,161 outputs,261 staged entries,0 commits, original18080/PID36063/static bytes and Docker resources.

## Task 1: Question ledger and approval binding

Files: new internal/platform/clarifications.go, clarifications_test.go, clarifications_postgres_test.go; update goals.go and service.go; expose onlypropose through internal/httpapi/mcp.go.

Interfaces: commandsclarifications.propose/answer from the spec; helperclarificationSnapshot(tx,*goal) returns current question batches and rejects unknown/unanswered state before approval. Store exact snapshot+digest in baseline approval, leaving existing criteria binding compatible.

- [x] Write tests usingreal Service.Command first: propose thenapprove returns409; Agent owner answer403; same answer replay returns original and changed answer409; revision change keeps history but rejects old reply; unknown fields/types and audit failure leave nopartial state. Run `rtk proxy go test ./internal/platform -run Clarification -count=1`, retain unknown-command red.
- [x] Implement ledger withscoped/current-authority checks, strict validation, Once/Remember and Store.Update; freeze after approval. Add optionalexpectedRequirementRevision check to baseline approval for workbench callers.
- [x] Run the above tests green. Verify twoPostgreSQL clients cannot overwrite first answer or race an open question through approval; real archive/restore preserves completed answers and nonempty retry rows.

## Task 2: Governed pre-baseline model assistance and planning input

Files: new internal/platform/clarification_generation.go, internal/integrations/clarifications.go and relatedtests; update integrations/model.go, model_context.go, worker/agent.go, httpapi/server.go, cmd/platform/main.go; new HTTP and Worker tests.

Interfaces: Service.BeginClarificationGeneration(ctx,actor,input)(request,new,error), CompleteClarificationGeneration(ctx,actor,requestID,questions)(Resource,error), FailClarificationGeneration(ctx,requestID,reason)error; Model.GenerateClarifications(ctx,actor,input)(Resource,error). Handler.GenerateClarifications matches that signature. Privatechat adds generation request ID; publicChat passes an empty ID.

- [x] Write HTTP missingroute and model boundarytests first; usea local HTTP provider returninga complete JSON envelope/valid usage. Assertions cover onecall per retrykey, normalChat still409 on drafts, human/policy/budget/current revocation/revision/approval conflicts and no questions on malformed output.
- [x] Persist generationintent before calling; completed retry reads result, started/failed retry409. Guard draft inference with current intent/human product permission inside charge reservation. Mark request failed on unusable resultwithout refunding charges or automatic replay; successful questions and intent completion commit together.
- [x] Bind charge to generation request; use the shared approved-snapshot selector in planning and the actual Worker agent loop, validate digest/structure and project/revision isolation with captured local model inputs.
- [x] Run focusedplatform/integrations/HTTP tests and thenfull Go race with dedicated PostgreSQL, vet/build/tidy; retain raw results and exact count.

## Task 3: Integrated question/answer workbench and delivery

Files: new web/src/Clarifications.tsx, web/tests/clarifications.spec.ts, docs/requirement-clarification.md, scripts/verify-workbench.py; update Delivery.tsx/api.ts/styles.css, package.json/playwright.config.ts and relevantdocumentation.

Interfaces: reuseworkspace resources, command runner and Form/Confirm. Current unanswered count disablesapprove; answer UI preserves actor/time and old revisionhistory. Generationrequest key is stable while itsdialog is open, and human explicitly initiates eachnew request after failure.

- [x] Write failingdesktop browser scenario for missingquestion workflow before UI implementation; useactual HTTP/SQL/auth, local deterministic modelonly.
- [x] Build the panel in the existingvisual system; test manual andgenerated questions, approval blocking/freezing, immutability, oldhistory, goal switching, read-only refusal, required fields, Escape/focus and no horizontaloverflow at both widths.
- [x] Run isolatedTypeScript/Vite and appropriate existingworkbench/membership/cost regressions; inspect final screenshots and review code sequentially. Keep originalpreview unchanged.
- [x] Make the standard npm browser command reproducible: isolate each suite/viewport and supply local model fixtures only where required. Keep the initial unconfigured-model failure and run all browser suites through the source-controlled runner.
- [x] Update validation/originalplan and the scopedtodo; verifypreservation, export six new deliverables and independently read back source/output hashes. Only complete thisincrement, retain fullproduct/P3 as active.

## Completion evidence — 2026-11-06

- Final Go race:373 top-level /1381 including subtests, no failed or skipped tests; dedicated PostgreSQL17.6 contention and archive/restore passed; vet/build/tidy-diff passed.
- Standard source-controlled npm browser runner:12 files /24 isolated suite/viewport groups /34 passing flows; no skips, retries or external model calls. TypeScript/Vite and24 main-thread-reviewed source/test/config files verified.
- Actual owned platform SIGKILL/restart retained running intent, reserved cost, nonempty retry row and one provider call; final binary matches the standard browser build.
- Preservation:379 original source fingerprints constrain this increment;394 current sources,161 old outputs,261 original staged entries,0 commits, original18080/PID36063/three static files,34 baseline containers/23 image references/15 volumes.
- Six uniquely named public artifacts exported; independent readback compared every archived source and all output hashes before this scoped completion. Completion ledger is refreshed into only this increment's own outputs and read back again.
- Complete only todo010. Full product/P3 remains active; no production deployment, signing, account/channel operation, paid model, real-model question-quality acceptance or external notification. Apple SDK probes remain69.
