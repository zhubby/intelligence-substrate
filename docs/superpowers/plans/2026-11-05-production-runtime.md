# Production Runtime Implementation Plan

> Execute sequentially with superpowers:executing-plans; the user's AGENTS instructions prohibit subagents. The approved product plan already authorizes implementation and local verification.

**Goal:** Run approved backend applications, retain operational health/incident evidence, and execute explicitly approved server rollbacks.

**Architecture:** A trusted Docker controller accepts the existing publication protocol, verifies an immutable Docker image archive and runs a restricted container. A separate read-only gateway forwards application traffic using the durable active-version pointer. The control plane owns deployment records, incidents and human rollback authorization; monitoring never counts as independent business acceptance.

**Tech stack:** Go, SQLite, Docker CLI, existing Go control plane/React workbench.

**Spec:** ../specs/2026-11-05-intelligence-substrate-design.md and the approved product's production-deployment, monitoring and server-rollback requirements.

## Global constraints

- Keep project/goal/destination mapping administrator-controlled; no repository code receives Docker or platform credentials.
- Bind activation to approved artifact SHA256/size, source commit, release binding and an operation key. Never automatically replay an uncertain deployment or rollback.
- Container images run non-root, with read-only rootfs, dropped capabilities, limits, no published host ports and a managed internal network. The separate gateway binds loopback and must route to the managed private container IP. No host directories or Docker socket enter an application.
- Stop the current writer before starting a replacement that shares persistent data. Support only applications whose validated data contract is unchanged; this is not a database migration engine or zero-downtime deployment.
- Monitoring remains available for the deployed version after requirements change; an old report cannot overwrite a newer activation. Health is not independent business acceptance.
- Rollback requires a human release decision bound to the expected active deployment and exact historical destination. Unknown results require explicit reconciliation, never generic task retry.
- Preserve the user's preview data and unrelated Docker images/containers/volumes. Local acceptance resources use a unique namespace and are stopped after verification; retain test data.

## Tasks

- [x] **1. Trusted runtime and gateway.** Create `internal/runtimehost/{config,archive,store,docker,engine,http}.go`, `cmd/runtime/main.go`, and tests. `Engine.Execute` consumes protocol-v1 publication fields plus rollback `expectedActive`/`rollbackTo`; returns state/externalId/url/runtime metadata. Archive tests reject traversal, multiple images, unbound content and unsafe config; real SQL/HTTP tests prove transactional activation, old-operation replay safety and gateway version headers. First run failing tests, then implement. Verify `rtk proxy go test -race ./internal/runtimehost`.
- [x] **2. Governed operations.** Create `internal/platform/runtime.go` and tests; update service dispatch, task hooks and publish completion. Commands: `runtime.report` (trusted publishing worker, persisted sequence), `runtime.rollback.propose`, `runtime.rollback.approve` (human), `runtime.rollback.reconcile` (human with report), `runtime.verify` (independent quality). Deployment/incident/rollback are normal project resources. Tests exercise stale observations, outages/recovery, revision changes, cross-project access, agent approval denial and uncertain rollback leases. Verify `rtk proxy go test -race ./internal/platform`.
- [x] **3. Worker and monitoring integration.** Add separately configured `rollbackProviders`; never reuse a publisher for rollback. Runtime monitor persists probes and retries control-plane report delivery without exposing credentials. Test gateway/application outages and real-worker request bindings. Verify `rtk proxy go test -race ./internal/worker ./internal/runtimehost`.
- [x] **4. Workbench and deployment.** Create `web/src/pages/Runtime.tsx`, embed it in the release view; show actual deployed version, health, incidents, rollback proposal/approval/reconciliation and independent operational verification. Add runtime config, gateway TLS sample, API/runbook documentation and sixth binary to builds. Verify TypeScript/build and desktop/mobile browser behavior.
- [x] **5. Actual Docker acceptance.** Add `scripts/verify-runtime.mjs`. Build scratch-based Go backend images offline; deploy v1/v2, persist real feedback, observe failures, execute approved rollback, ensure data remains and replay cannot reactivate superseded operations. Verify actual gateway HTTP/Chrome behavior, final Go vet/race including isolated PostgreSQL, browser tests, cross-compilation, formatting and links. Record exact success and remaining external/native/channel gaps in `docs/validation.md` and a user-facing output report.

## Completion boundary

These tasks advance the original product; they do not complete multi-store/native acceptance, enterprise HA/IAM or the two formally delivered applications. Keep the full objective active while those requirements lack authoritative evidence.

## Verification ledger — 2026-11-05

- All five scoped tasks implemented and checked; full product objective remains active.
- Go vet and 77 top-level tests plus subcases pass with race detection and the isolated PostgreSQL instance. No test was skipped; the dedicated database process was stopped afterward.
- All six local binaries and Linux/Windows amd64 command builds pass. TypeScript/Vite build and npm audit (zero vulnerabilities) pass.
- Four Chrome desktop/mobile scenarios pass, including actual report-file upload through the workbench. Runtime metadata and approvals in these browser fixtures are synthetic.
- Actual Docker/local TLS/browser acceptance: `work/runtime-smoke-IReQ78/result.json`. Verified non-root override, read-only rootfs, resource limits, internal network/no host ports, v1/v2 writes, incident/recovery, approved rollback, two durable records, exact version headers, gateway restart and receipt replay safety. Test identities and pre-release gates are synthetic.
- Existing distribution/publisher CLI acceptance: `work/release-smoke-kFMYiP/result.json`; version 1.1.0 and renewed signed index sequence 3.
- Live preview restarted on 18080; original four resources, one identity and credential hashes preserved. Original credentials still log in in Chrome; no test deployment was injected.
- Configuration JSON, local documentation links and diff whitespace checked. Source remains local; no commit/push or remote CI claimed.
- The formal platform container image remains blocked by the existing registry proxy failure. Scratch application images were built offline and actually run; these are different acceptance results.
