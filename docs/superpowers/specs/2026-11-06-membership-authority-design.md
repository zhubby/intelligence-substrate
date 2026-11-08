# Enterprise membership and delegated authority

This increment implements the approved platform's organization/project permissions and offboarding boundary. The original platform implementation and continued work authorize this implementation; it does not authorize disabling real users, rotating existing credentials, or deploying to the original preview.

## Product behavior

Project owners can update a registered active human member's roles or remove their project membership with an explicit reason and revision. Removed membership remains visible for audit and can be reinstated through a new grant. The last active registered project owner cannot be removed, demoted or globally disabled until a replacement owner exists. Concurrent changes serialize on the existing SQLite/PostgreSQL transaction boundary.

Every changed role set or revoked membership atomically revokes that person's project Agent/Worker tokens. Identical role sets do not revoke tokens. Rejoining or promotion never resurrects deleted credentials. Other projects and unrelated issuers retain their credentials. A global identity disable revokes all of that identity's credentials and active memberships in one transaction, with project-scoped membership audit records. Self-disable remains forbidden.

Member grants require an existing enabled identity and an existing project. Roles must be a distinct, complete supported string list. Existing grant callers remain compatible; when an expectedRevision is supplied, stale updates fail. New members.revoke requires id, expectedRevision and reason. Governance is a human owner decision and is not exposed as an Agent MCP action.

## Delegation and transaction boundary

Three options were considered: revoke rows only; dynamically intersect delegated roles only; or combine transactional revocation with current authority checks. The combined design closes both future-request reuse and already-authenticated stale actors without allowing a later promotion to revive a revoked token.

The HTTP Auth layer attaches the authenticated credential hash to an internal Actor field excluded from all JSON. Tx.CurrentActor reloads the exact credential and enabled identity, verifies expiration and stored actor identity/scope, and loads current Worker roles. Token issuers must currently be administrators or active project owners. Every governed command and project access check revalidates that authority inside its business transaction. Internal trusted CLI/test Actors without the authentication marker retain the existing service interface; HTTP clients cannot supply that marker.

Token issuance moves permission checks, current issuer/Worker validation, credential insertion and audit into one Store.Update transaction. A simultaneous owner demotion either revokes an already-issued token or causes issuance to fail. Auth session creation also rechecks enabled identity inside its insert transaction. Identity disable reads current identity data in that transaction so it cannot overwrite a newer OIDC binding.

Revoking access does not prove a previously dispatched tool or remote publication has stopped. Existing lease expiration and external-result reconciliation remain authoritative; no success, automatic resubmission or native-process termination is inferred. Requests already authorized for an external effect cannot be recalled. Newly authorized business mutations after revocation commits are rejected, including with a cached authenticated Actor.

## Workbench and compatibility

Reuse the existing governance member list. Show member state, current roles, role editing and a removal dialog with a required reason. Role edits preserve a full role list; the UI supplies the displayed revision. Removed rows retain their historical role labels and offer explicit regrant for eligible identities. Confirmation explains immediate credential loss and that new authorization is required. Refresh project selection when the current user's project access disappears.

Do not add a separate identity dashboard, SCIM, IdP group provisioning, MFA, account recovery, administrator promotion or password reset. Those remain separate enterprise capabilities. Existing OIDC subject binding, human approval, independent quality, source/release recovery and backup boundaries stay enforced.

## Verification and preservation

Test real identity/session/token requests, stale authenticated actors, cross-project isolation, role/revoke validation, last-owner protection, rollback and SQLite/PostgreSQL concurrency. Browser tests use actual control-plane APIs at desktop/mobile widths and verify dialogs, errors, keyboard dismissal, durable membership state and credential rejection. No paid models, real-user changes or production/store operations.

Preserve 135 prior outputs by SHA256, all 261 staged entries, the unborn branch and PID 36063/18080. Build new Web assets only in the task-owned directory. Export source, Chinese report, runbook and verifiable evidence; keep the full platform goal and P3 open. Apple SDK probes still return 69; never accept agreements for the user.
