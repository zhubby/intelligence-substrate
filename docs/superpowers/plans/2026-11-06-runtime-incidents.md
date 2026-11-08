# Runtime Incidents Implementation Plan

> Execute sequentially with superpowers:executing-plans. The user's AGENTS instructions require all work in the main thread. The approved product plan authorizes this runtime-monitoring increment.

**Goal:** Persist monitor silence and human incident ownership, with recovery driven by trusted observations.

**Architecture:** Extend existing incident resources in serial Store.Update transactions; run a control-plane watchdog independently of publishing workers; expose acknowledgement through the existing governed command API and workbench.

**Tech Stack:** Go, SQLite/PostgreSQL, React/TypeScript, existing Playwright.

**Spec:** ../specs/2026-11-06-runtime-incidents-design.md

## Global Constraints

- Signal expires after 90 seconds; the watchdog runs at startup and every 15 seconds. Failure/recovery thresholds remain 3/2.
- No paid calls, external notifications, real-account/channel writes or unattended release/rollback decisions.
- Preserve the original preview PID 36063/18080 and all web/dist bytes, original 261 staged entries, existing source and outputs outside this increment.
- All local commands start with rtk. Build/test resources belong to work/runtime-alerts-p8s_05oz. No commit/push or subagents.

## Task 1: Durable incident state and authorization

Files: create internal/platform/runtime_incidents.go, runtime_incidents_test.go and runtime_incidents_postgres_test.go; modify runtime.go and service.go.

- [x] Write tests proving silence opens one incident after expiry/initial grace; a second independent scan/restart cannot duplicate it; application incidents remain distinct.
- [x] Run `rtk proxy go test ./internal/platform -run 'TestRuntimeIncident|TestRuntimeSignal' -count=1`, capture the missing-behavior failure.
- [x] Implement `Service.ReconcileRuntimeSignals(context.Context) error`, using a private transaction function accepting a time for deterministic boundary tests. Match incidents by project/goal/application/worker/type; create and resolve with system audit records.
- [x] Route `runtime.incident.acknowledge` to a human-only release/quality scoped command; require id/key/note, apply Once/Remember, preserve open status and first ownership. Reject changed duplicate content, new attempts on owned/resolved incidents, and stale authority.
- [x] Integrate typed health incidents and signal recovery in reportRuntime. Reject a higher sequence whose checkedAt does not advance; update the existing gap assertion to distinguish health and signal incidents.
- [x] Run the targeted race tests, including two PostgreSQL clients in an isolated database. Assert only one event and first ownership under concurrency, and verify archive/restore preserves incidents and idempotency.

## Task 2: Control-plane lifecycle

Files: modify cmd/platform/main.go; add HTTP acceptance in internal/httpapi/runtime_test.go.

- [x] Add authenticated HTTP tests for successful acknowledgement and required scope/human/CSRF enforcement, run before implementing command exposure if Task 1 has not already exposed it.
- [x] Start a cancellable watchdog immediately after platform initialization, use a 10-second context per scan and a 15-second ticker; log errors and join the goroutine before Store.Close.
- [x] Build is-platform into the task work directory and run an isolated process with synthetic expired deployment data. Observe an event without any worker traffic, stop/restart, confirm no duplicate, resume reports and confirm recovery.

## Task 3: Workbench and verified delivery

Files: create web/src/pages/RuntimeIncidents.tsx and web/tests/runtime-incidents.spec.ts; update Runtime.tsx/styles.css, releases.spec.ts, API/runtime/operations/validation docs, README and original plan ledger.

- [x] Add browser scenarios using test identities and synthetic deployment metadata: ownership form/required note, Escape/focus, actual backend acknowledgement, recovered history, duplicate/conflict and read-only permissions.
- [x] Implement the existing-system design: typed active events, ownership details, stable per-form retry key, collapsible recovered history, wrapping layout. Reuse shared components.
- [x] Run TypeScript and Vite with an isolated output directory; run Chrome at 1440x1000 and 390x844 and inspect screenshots. Do not rebuild/restart the original preview.
- [x] Run relevant full Go race/vet and PostgreSQL tests, check documentation links/formatting, verify preservation manifests. Record precise evidence and limitations.
- [x] Export source/report/runbook/evidence under the authorized outputs directory, read back ZIP and hashes independently, mark only todo 008 complete. Keep the overall product goal active.

## Final verification ledger

- Full final race suite: 342 top-level /1296 including subtests, no test skips; PostgreSQL17.6 concurrent watchdog/first-ownership and nonempty-idempotency restore passed. go vet, platform build and module tidy diff passed.
- Final Chrome desktop/mobile ownership/recovery and existing release/rollback/report/verification flows passed (4 comprehensive flows); separate captures show real watchdog-created simultaneous incident types. Actual deployments/worker observations remain synthetic fixtures.
- Final process drill: 8 checkpoints, actual 90-second silence plus one scan interval, restart dedup, expired-100-second report HTTP422, separate recovery and preserved independent-acceptance requirement. All task services stopped.
- Main-thread review reproduced and fixed the old 2-minute report window's false-recovery edge; red/green evidence retained. No subagents or independent-code-review claim.
- Existing release regression reproduced an ambiguous locator after adding incident cards. Deployment cards now have explicit accessible names and regression targets the exact deployment; both viewport flows pass in fresh states. Test screenshot paths honor an isolated directory.
- TypeScript/Vite use an isolated outDir; original preview, Git staging and existing outputs preserved. Packaging includes a per-file manifest and independent readback. P3 and the overall product objective remain active.
