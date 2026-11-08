# Governed Source Integration Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in the main thread. No subagents, commit/push or unrelated service changes.

**Goal:** Integrate selected Agent results, resolve conflicts with exact decisions, then build and independently verify the combined application.

**Architecture:** Approved code tasks opt into sourceMode=integrate. Immutable bindings select parents, trusted Git plumbing prepares a private tree, and the existing durable source snapshot/CAS produces the final multi-parent commit. Independent build/test and repair remain the acceptance mechanism.

**Tech Stack:** Go, Git merge-tree/commit-tree, SQLite/PostgreSQL, HTTP worker API, React/TypeScript, Playwright.

**Spec:** ../specs/2026-11-06-source-integration-design.md

## Global Constraints

- Every shell command starts with rtk; no subagents, commit/push, unrelated cluster/account use, or Apple agreements.
- Maximum eight unique integration parents, at most 100 conflict paths; source archive retains 64 MiB / 20,000 entries. Larger work uses staged integration tasks.
- Default non-integration source binding stays strict; retries preserve input. Production approvals and quality gates remain protected.
- Final source parents are original selected commits. Intermediate conflict trees are private preparation, not final history.
- Deliverables use new names under the existing outputs directory; all earlier outputs and the preview process are preserved.

### Task 1: Binding and trusted preparation

Files: platform/task_source.go, new platform/source_integration.go and source_integration_test.go, goals.go, service.go, adaptive.go.

Interfaces: source binding adds sourceMode and integrationParents; sources.prepare returns the current run with integrationId/workspaceCommit/conflicts/resolutionDigest. Preparation records are immutable per binding.

- [x] Add real Git fixtures with left/right code results and an integration task. Assert claim succeeds only for the approved mode, preparation preserves both files and content conflicts expose actual paths; repeat prepare returns the same commit/object.
- [x] Run `rtk proxy go test ./internal/platform -run TestIntegration -count=1` and verify missing behavior fails.
- [x] Validate sourceMode, bind all parents, implement lease-bound preparation with machine-parsed merge-tree and protected refs, reject unrelated histories, excessive inputs/conflicts and duplicate integration splitting.
- [x] Verify clean/conflict/deletion/binary preparation, retries, scope and strict-default tests pass.

### Task 2: Exact conflict decisions and durable snapshots

Files: source_integration.go/tests, source_commit.go/tests, tasks.go, HTTP source metadata, defects.go.

Interfaces: sources.resolve accepts resolutions[{path,digest,executable,deleted}]; snapshots pass integrationId/resolutionDigest and infer parents from protected state.

- [x] Add failing snapshot tests for unresolved markers, missing/wrong/stale resolution decisions, file-mode mismatch and text-commit bypass. Assert the valid commit has original input parents and excludes preparation commits.
- [x] Implement per-run decision maps/digests, verify uploaded bytes/modes/deletions, allow legitimate unchanged merge trees, preserve prepared/CAS/replay behavior and require current-run integration completion.
- [x] Reset mode/runtime binding in repairs and use a fresh repair branch, then verify existing independent repair/rebuild/retest behavior and new integration result ancestry.

### Task 3: Worker and actual delivery flow

Files: new worker/integration.go/tests, worker/runner.go, agent.go, source.go, model prompt, Delivery.tsx and browser test.

Interfaces: the controller prepares selected inputs, exposes read_input/take_input/resolve actions, submits clean integrations directly and records explicit resolutions after approved commands.

- [x] Add failing real HTTP/native tests: actual parallel code results → clean/conflicting integration → branch movement → build → independent QA; assert final source bytes, parent history and evidence SHA.
- [x] Implement controller preparation, private selected-version archives, bounded reads/version choices, explicit decisions and automatic clean submission. Add context/actions to the Agent and approved mode to model planning.
- [x] Expose preparation and conflict decisions in existing details, then verify keyboard/mobile layout and the actual API/worker chain.

### Task 4: Complete verification and concrete exports

Files: API/worker/source/validation/README docs and ignored work verification/export scripts.

- [x] Run full vet/race with a fresh isolated PostgreSQL cluster, build all seven Go commands for macOS/Linux/Windows, build the workbench and run relevant full browser checks.
- [x] Review changed files in the main thread for correctness, security and simplicity, including exact replay, per-run decisions, private intermediates and authority boundaries.
- [x] Update docs, mark actual checks, export a new source ZIP/report/runbook/evidence/screenshots, compare every ZIP file to the workspace, verify links/hashes and preserve prior outputs. Leave the full product goal active.

Final evidence: `work/source-integration-3k2hcpir/result.json` (159 top-level / 328 with subtests, 21 executables, 8 browser scenarios; fresh PostgreSQL stopped). Main-thread correctness/security/simplicity review, no independent reviewer. Report and byte-verified source archive use the new source-integration output prefix; prior 59 deliverables and 18080 are preserved. Full product goal remains active.
