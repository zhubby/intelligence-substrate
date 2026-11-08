# Task source binding implementation

Execute sequentially in the main thread using executing-plans, TDD and verification-before-completion. The existing approved product authorizes implementation; no subagents, commit/push or live cluster modification.

Spec: ../specs/2026-11-06-task-source-binding-design.md

- [x] Tests first: real Git source results, branch movement, graph traversal and split aggregates, explicit sourceFrom/repository/commit selection, unmerged inputs, stable retry, forged or stale provenance and adaptive boundaries.
- [x] Implement claim-time resolver, versioned binding/conflict metadata, reserved-field/graph validation, run-bound producer checks and repair binding reset.
- [x] Update worker archive selection and preflight; verify actual HTTP source → branch advance → build → independent quality preserves the selected source. Expose useful source/blocked provenance in the task detail already available to users.
- [x] Full Go vet/race with isolated PostgreSQL, cross builds and main-thread review. Update API/worker/validation docs, export new source/evidence/report with previous outputs preserved and leave the overall product goal active.

Verification: work/task-source-es9nw4ng/result.json — 147 top-level / 275 including subtests, no failed or skipped tests; isolated PostgreSQL stopped; 21 Go executables, full workbench build and 6 real Chrome scenarios passed. Main-thread review and byte-verified new exports recorded in the same evidence directory. Overall product remains incomplete.
