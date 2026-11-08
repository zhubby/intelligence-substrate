# Durable Agent progress

Implements the approved requirement that ordinary Agent work survives interruption without silently repeating actions. This is a sequential P2 increment, not full P3 acceptance.

## Contract

Ordinary no-command analysis/code/design claims advertise progress version 1. Before each nonterminal action the trusted Worker persists an immutable, idempotent intent bound to its active lease, task recipe, requirement, plan, source and previous confirmed progress. A second action or inference cannot pass an outstanding intent. Fixed commands, signing and publication keep their existing protocols.

After execution, code/design save a complete bounded source archive including modes and deletions. The Worker then confirms the exact observation and archive. The confirmation atomically advances the task's progress pointer. Analysis confirms its observation without a filesystem archive. Neither a checkpoint nor a tool observation is source adoption, quality evidence or approval.

Replacement claims validate the current authority and exact saved bytes, retain the cumulative cost/time/call envelope, and restore only confirmed progress. Fresh inference receives a bounded trace of public actions and actual observations as untrusted data; provider reasoning and conversation internals are not persisted or replayed. Durable inbox delivery remains independent.

An interrupted intent fences retry and replacement. A current human project owner must disable the original Worker, wait for its lease to become inactive, independently stop/reconcile any process or external side effect, and record an explicit decision and reason before retrying from the last confirmed checkpoint. This never asserts physical termination or exactly-once external effects. Model charge and source/sandbox reconciliation still apply separately.

Explicit collaborator waits retain their existing snapshot and supersede the ordinary-progress pointer. Source submission/design publication/knowledge finish retain their own completion semantics; terminal actions are not replayed. Corrupt/stale snapshots, authority changes and unresolved inference charges fail closed. Portable backups include checkpoint bytes and audit history.

## Acceptance

- Concurrent/idempotent prepare, exact completion, streaming reauthorization and audit rollback.
- Pending intent denies inference, retry and replacement; reconciliation requires current human owner, disabled Worker, inactive lease and explicit external reconciliation.
- Exact files, deletion and executable modes restore after actual Worker/control-plane loss; unknown actions are never automatically replayed.
- Source/plan/requirement changes and corrupt bytes cannot resume; cumulative allowance and independent QA remain enforced.
- PostgreSQL, portable restore, HTTP boundaries, actual local process drills and source/output readback. Local deterministic provider evidence does not establish real-model business quality or full P3.
