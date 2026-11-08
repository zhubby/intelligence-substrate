# Durable analysis collaboration waits implementation plan

> Execute sequentially in the main thread using superpowers:executing-plans; the user's AGENTS instructions prohibit subagents. Preserve the unborn branch and existing index; do not commit or publish.

**Goal:** Let approved analysis Agents release resources while awaiting an exact durable reply and continue within their existing authority and limits.

**Architecture:** Transactional `message_wait` records and inbox receipts belong to the delivery control plane. A Worker journals wait submission, exits its disposable workspace, and later performs fresh inference from the frozen analysis source and bounded summary. Existing REST/MCP authorization, model billing and human gates remain authoritative.

**Tech stack:** Existing Go/SQLite/PostgreSQL, React/TypeScript and Playwright; no new dependencies.

**Spec:** ../specs/2026-11-07-message-wait-design.md

## Global constraints

- Analysis tasks only, no fixed command and no executed command before suspension. No source/session replay claims.
- At most 3 waits, 24 inference steps per suspension chain, 60–86400 seconds per wait. Preserve original total task budget and active timeout.
- Exact recipient, causation, task, requirement, approved plan and source binding. Coordination messages never authorize decisions.
- No paid model, store/production action, original preview deployment, Git staging/commit or changes to prior deliverables.

## Tasks

- [x] 1. Add failing platform tests using `newMessageFixture`: submit `tasks.wait`, assert suspended run/no lease and waiting task; send an exact `messages.send` reply and assert one inbox receipt, pending task and replacement Worker continuation. Add wrong sender/task/cause/type, early reply, lost-response replay, cancellation/timeout, capacity, rollback and stale-scope cases. Run `rtk proxy go test ./internal/platform -run MessageWait -count=1` and retain the expected unknown-command failures.
- [x] 2. Implement `internal/platform/message_wait.go` for command validation, immutable wait bindings, exact reply receipt/wake, maintenance, continuation allowance and context. Wire service/messages/tasks/goals/adaptive/main maintenance, protect new internal fields and share inbox receipt creation. Rerun platform tests; record real remaining budget and active deadline, unchanged attempts, closed waits and rejected unauthorized input.
- [x] 3. Add failing real Worker tests for wait journaling and deterministic local provider suspend/responder/resume. Extend `completion` dispatch with a validated wait operation, add Agent wait action only before commands, and inject frozen fresh-analysis continuation. Run Worker/integration/HTTP tests; expose `tasks.wait` through governed MCP and verify current auth and sanitization.
- [x] 4. Add failing browser flows for work-item wait detail and exact human reply, permission/error handling and stale dialog context. Implement focused `MessageWait` UI using existing forms/context/MessageContext and add `waiting_message`/`suspended` status labels. Run desktop/mobile flows with real API fixtures; inspect screenshots.
- [x] 5. Add dedicated PostgreSQL contention and actual backup/restore tests. Execute full Go race with an isolated PostgreSQL instance, vet/build/tidy/format, TypeScript/Vite and complete standard browser suites. Independently inspect exported SQL/archive wait/inbox state. Review security/correctness/simplicity sequentially and fix concrete findings.
- [x] 6. Document supported semantics and limitations; export source/report/evidence/screenshots under a unique outputs prefix, scan private values and independently read back hashes/contents. Verify original index, 245 outputs, preview, Docker and AVD baselines, stop task-owned services, then mark only todo 022 complete.

## Verified checkpoint

Final evidence is under `work/message-wait-3t29vhme/`: `regression-final` passes 508 top-level / 1816 with subtests, including real PostgreSQL; vet/build/tidy are clean. `browser-all` passes 18 files / 36 groups / 72 flows; after reproducing and fixing stale task-dialog state, `browser-review-final` passes 6 affected files / 12 groups / 20 flows. No skips, failures or browser retries. Tests use a local deterministic provider, with actual Worker HTTP loops and synthetic billing; no paid calls or real-model-quality claim.

The original process drill only killed RTK's wrapper, leaving its child alive. That restart claim is explicitly invalidated in its retained correction record. `process-drill-verified` checks the actual listener's PID/process group, port closure before restart, a different restarted PID, wait restoration, actual 60-second expiration and closed port after completion. Independent Python tar/SQLite readers confirm immutable backup rows and accurate wait/receipt/continuation after restore. Preservation verifies all prior output/index/preview/Docker/AVD baselines. Six new outputs are exported and independently read back before and after completing only todo 022; runtime databases, traces, credentials and SDKs are omitted.

The current Xcode version query succeeds, but both Apple SDK probes and all four actual reference helper calls still exit 69 for the unaccepted license. No Apple artifact exists, no license was accepted by the Agent, no global Xcode selection was changed, and the original preview and overall P3 acceptance remain unchanged.
