# Workspace-backed collaboration waits

Approved scope: extend the existing durable collaboration loop to code and design tasks. A task can ask a named collaborator, save its selected workspace, release the execution lease, and resume fresh inference on another Worker after the exact reply. This advances the approved dynamic development/quality workflow; a checkpoint grants no completion, source adoption, quality or publication authority.

## Binding and storage

- `workspace_checkpoint` is a distinct resource, never an artifact. A run uploads one immutable full portable tar tree, bounded by the existing 64 MiB/20,000-entry source archive rules. Store its verified bytes in private CAS and retain the original task recipe, requirement/plan, source and authoritative source binding.
- `POST /api/v1/runs/{id}/checkpoint` accepts application/x-tar and one bounded strict `X-IS-Checkpoint` header (leaseToken, repositoryId, commit). Validate current Worker, task, lease, deadline, source, target head and allowance before body consumption and at registration. Replays require exact bytes and binding; no credential enters durable checkpoint metadata.
- `GET /api/v1/runs/{id}/checkpoint` requires the current Worker lease via `X-IS-Lease`. It returns only that run's authoritative continuation checkpoint after checking current authority and CAS integrity. Humans inspect checkpoint metadata in the task history; no generic artifact route exposes its content.
- Backup/restore includes the resource and CAS and verifies checkpoint bytes along with ordinary artifacts.

## Suspension and recovery

- Existing analysis waits stay unchanged. Code/design waits require a checkpoint belonging to the current run and exact declared source. The wait transaction binds its ID/digest and releases the lease atomically. Existing request causality, three waits, 24 cumulative steps, cumulative active time/budget, unresolved charges and current baseline/plan rules still apply.
- Resume fetches the original fixed source (or prepared integration) to reconstruct original tracked paths, then replaces the disposable tree with the verified checkpoint. Add checkpoint paths to tracking and preserve explicit executable bits, so deletions and later ignore-rule changes cannot silently lose source. No conversation, hidden reasoning, process, credentials or command is restored/replayed.
- Original base commit, source binding, target branch and expected head remain fixed. A changed target blocks continuation. Suspension creates no source commit/change, build, design proposal or quality evidence. A failed/uncertain checkpoint upload must not invoke stopped-source branch submission.
- Code may suspend after a completed command; only selected source files survive. Running/background processes and external side effects do not. Analysis still cannot wait after commands. Design checkpoints allow only nonexecutable HTML additions/edits of 1 B–2 MiB relative to the original tree, preserving every other file exactly.

## Integration and authority

- Integration preparation survives; save the controller's exact preparation ID and run-owned conflict decisions in checkpoint metadata. Resume copies them only from the checkpoint bound by the valid wait to the new run, after checking preparation/source identity. Normal failure retries never inherit arbitrary prior decisions.
- Partial conflict resolution may be checkpointed. Final source submission still validates every conflict decision against final bytes/modes/deletions and rejects unresolved paths or conflict markers. Editing a resolved path requires resolving it again.
- Any attempted source submission freezes the source-operation path and forbids a new checkpoint. Requirement/plan/recipe changes, cancelled work, revoked Worker/role, expired lease, corruption and changed source prevent restore. Wait/source failure stays explicit and reviewable.

## Evidence

Use real HTTP/Git/CAS/Worker tests with deterministic model responses: binary source, deletion, executable modes, tracked ignored files, two suspensions, replacement Worker, design proposal, partial integration decisions, early reply/lost acknowledgement, budget bounds and archive/authority attacks. Exercise PostgreSQL concurrent replay and backup→SQLite restore. Verify desktop/390px wait history and keyboard/focus. Preserve existing index, outputs, original preview, Docker and AVD inventory; record actual environmental differences.
