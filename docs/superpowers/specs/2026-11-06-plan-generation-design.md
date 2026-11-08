# Durable Governed Plan Generation

## Scope and authorization

Continue the approved Intelligence Substrate product plan in the current checkout, sequentially in the main thread. This increment hardens the existing approved-baseline-to-plan flow. It does not claim autonomous quality scheduling or complete formal delivery.

Preserve port18080/PID36063 and its backend/static bytes,261 staged entries and zero commits,173 existing outputs,37 containers/23 image references/15 volumes. Do not stage/commit/push, use paid models, accept Apple agreements, notify externally, or perform real store/production actions. Apple probes still return69.

## Problem and alternatives

`POST /api/v1/plans/generate` currently accepts only a goal ID. It snapshots context outside a transaction and creates a proposal after inference, without a durable request or a replay key. A lost response or process restart can repeat inference and cost. Changes to knowledge or repository inputs within one requirement revision can silently produce a stale proposal.

Use a durable intent in the existing authoritative SQL/audit/idempotency store. A process-local lock would not survive restart or multiple control-plane clients. Reusing the draft-requirements lifecycle would impose its unapproved/human-product semantics on approved engineering planning. Keep a focused plan lifecycle and reuse the existing model gateway and proposal validation.

## Contract and authority

`POST /api/v1/plans/generate` requires exactly `{goalId,expectedRequirementRevision,key}`. A key is a nonempty string of at most160 bytes; revision is an explicit positive integer. A request without either is rejected. Scope idempotency by goal and current identity; altered content under the same key conflicts.

Human and project Agent identities with product, architect or developer access may generate a proposal. Worker identities cannot invoke this detached lifecycle. Current credentials, identity and project membership are rechecked in transactions at intent creation, model admission and completion. There is no public start/complete/fail command, and public chat fields cannot select the trusted intent path. Existing manual/MCP proposal and human architect approval remain available.

## Durable state and snapshot

`plan_generation` transitions from running to succeeded or failed. Persist before network with requester identity/name/kind, requirement revision, canonical input and its SHA256. Maximum200 requests per goal. Do not record a raw API credential.

Build the snapshot in one transaction using current approved requirements, human-reviewed brief/provenance, frozen approved clarification decisions, budget, sources, active plan, versioned project knowledge, repository heads/default branches and enabled worker runtimes/roles/capabilities. Include baseline approval identities/bindings. Exclude spent amounts, charge history, heartbeat times and unrelated task progress so inference itself does not invalidate its input. Use deterministic resource ordering and canonical JSON maps.

At admission/completion reconstruct the same snapshot. A requirement change, approval change, active-plan change, knowledge edit, repository head change, worker disable/capability change or snapshot tampering rejects the result. Ordinary spend or heartbeats do not. No model call takes place in a SQL transaction.

## Model and completion

The integration sends only the captured input. Model policy, output/context bounds, rate limit, call count and budget remain the gateway's authority. Charges contain `planRequestId` and requirement revision. A trusted request cannot reserve a second charge.

Require one complete usable response and a settled completed charge. Parse only summary, optional risk and tasks; reject unknown top-level authority fields and malformed/oversized text. Reuse existing DAG, source, command, project-binding and task validation through `planCommand` in the same transaction as intent completion/audit. Bind the proposal to generation ID, captured input digest and charge. Include provenance in its approval binding. The proposal is not activated, and no execution task or approval is created.

On success an exact replay returns the original proposal, including its current active/stale status. Running/failed replays return409 without another network call. A failed/late result retains its actual charge and a failed intent. A killed process may leave running/reserved records; restart does not automatically resend, recover the provider result or resume a model session. A deliberate new request uses a new key after reviewing records and cost.

## Workbench

Keep the existing delivery design. Generate opens a confirmation containing captured goal/revision, budget impact and repeat guidance. The key and goal are fixed for the dialog lifetime; retries in that dialog reuse the key. Refresh records on both success and failure. Restrict controls to current roles; manual submission remains available. Show generation history with state, requirement version, requester/time and linked proposal/charge information. Mark generated proposals with their provenance. Keep separate human architect approval, keyboard/Escape/focus behavior and390px layout.

## Evidence

First capture failing replay/late-context tests and missing confirmation UI. Verify concurrent same-key attempts with one provider request, invalid/truncated/transport failures, policy/budget/role/credential revocation, snapshot changes, audit rollback and stale-success replay. Exercise real HTTP/CSRF and Agent credentials; lifecycle commands remain unavailable. Two PostgreSQL connections compete for the same intent and real backup/restore retains completed/running intents and nonempty idempotency records. Run an owned-process SIGKILL/restart drill after the local provider receives a request. Use only deterministic local providers; this is no claim about real model planning quality.

Run Go race with a dedicated PostgreSQL cluster, vet/build/tidy, types/isolated Vite build, full browser runner, formatting/links and scoped state preservation. Export and independently read back source, report, runbook, evidence and two screenshots. Mark only this increment complete. Overall P3 remains open.
