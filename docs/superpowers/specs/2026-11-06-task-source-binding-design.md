# Immutable task source inputs

The approved delivery platform must build the source produced by its approved work graph. `dependsOn` currently only orders tasks, while workers discover a mutable branch at execution. Split parents can succeed without one source result. This increment makes source selection authoritative and durable at claim time.

## Resolution

For code/build/security tasks, resolve the nearest successful code/build results in the same goal, plan and requirement revision, traversing analysis/design and split aggregates. An optional approved `sourceFrom` list selects dependency ancestors explicitly; each entry must be reachable through dependsOn. Explicit repository selection scopes inputs to that repository. Otherwise multiple source repositories require a declared selection.

Successful result provenance includes task, run, repository and exact commit. Source-producing tasks must have exactly one valid successful execution. A selected commit must contain all selected same-repository inputs by Git ancestry. An explicit commit must satisfy those inputs; otherwise use the unique input commit that contains all others. Unmerged inputs become a blocked task with structured sourceConflict; no branch guessing or automatic force merge occurs. A future integration workflow remains part of the full product objective.

With no producing dependencies, pin the explicitly approved commit or current configured source branch at claim. A root code task may create its project repository if none exists, matching the existing automatic application-repository behavior. Ambiguous repositories require selection. Code targets remain their approved feature branch (default agent/{goalId}); an existing target must match the selected base. Builds use the selected commit regardless of later branch movement.

## Persistence and authority

Persist a versioned sourceBinding (inputs, repository, commit, target branch, expected head, task/plan/revision and canonical digest) in the task and lease snapshot. On retry preserve and validate the original binding. Reject external injection of binding/conflict state. Once bound, dependencies and source selection cannot be rewritten by reorder/split; use new approved work for a different source. Repair/rebuild generation removes copied runtime binding state and binds its new approved commit.

Workers read the pinned archive. Code workers check their expected target before running tools and report conflicts before model work. Tool-generated commits must descend from the bound base and belong to the current run; registered build artifacts and successful build results must match the bound commit. Ordinary legacy in-flight runs retain existing completion semantics.

## Verification

Real Git/SQL tests cover source frontier selection, explicit selection, ancestry dominance, stale branches, stable retries, split aggregates, invalid/missing results, cross-plan/revision/repository isolation, reserved fields and adaptive edits. Real HTTP/native worker tests advance a branch after development and prove the build and independent quality still use the producer's exact source. SQLite and isolated PostgreSQL exercise transactional claims. Compile all Go commands on native/Linux/Windows. Preserve preview and all existing exports; no commit/push, unrelated cluster/model use or native signing claims.

This is an additional concrete step toward P2/P3; it does not complete the approved full product or establish live model/AX/application delivery.
