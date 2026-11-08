# Complete source snapshot implementation

> Use executing-plans and TDD sequentially in the main thread; no subagents, commit/push or live cluster modification.

**Goal:** Commit the complete source produced by a governed code task, including binary assets, deleted files and executable modes, with recoverable provenance.

**Architecture:** Shared safe tar utilities feed an authenticated streaming endpoint. Git plumbing writes raw objects and a durable operation precedes the branch compare-and-swap. Worker metadata stays outside its replaceable execution tree; a final deterministic snapshot replaces text-only completion submission.

**Tech stack:** Existing Go, native Git, database/sql, SQLite/PostgreSQL; no new runtime dependency.

**Spec:** ../specs/2026-11-06-source-snapshot-design.md

- [x] Safe source archive: tests for exact binary bytes/modes, root entries, size limits and malicious/ambiguous paths; implement `internal/source` tar/path helpers.
- [x] Governed commit: real Git tests for replacement/deletion/mode, conflict, replay, prepared recovery and lease restrictions; implement platform snapshot operation and HTTP endpoint with bounded private metadata header.
- [x] Actual agent workspace: tests for ignored/tracked/generated files, preserved modes and tool-created changes through real HTTP; implement trusted external index, streaming upload, delete/write-mode actions and completion.
- [x] Verify: Go vet/race including a new isolated PostgreSQL cluster, actual worker-to-Git-to-independent-quality acceptance, native/Linux/Windows builds and scoped review. Update API/worker docs, validation and progress ledger; export source/evidence without replacing prior outputs.

Files: `internal/source/{archive,path}.go` and tests; `internal/platform/source_commit.go` and tests; `internal/httpapi/source.go` plus server route/tests; `internal/worker/source.go` and tests plus agent/runner/client integration. Preserve existing text-commit and release APIs.

The test model is deterministic and must be described as such. Snapshot/Go cross-compilation cannot establish live AX isolation or native application delivery. The full product objective stays active.
