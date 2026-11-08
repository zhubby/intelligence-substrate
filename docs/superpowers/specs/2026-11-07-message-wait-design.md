# Durable analysis collaboration waits

This increment implements the approved platform's structured collaboration and resource-release requirements. It does not complete P2/P3 acceptance or introduce source/session checkpoint recovery.

## Behavior and authority

An analysis Agent with no fixed command may explicitly wait for one request, clarification or review it sent during its current valid run. The request must address a different current task or a project collaborator. The Worker must not have executed a command in this inference session. Code/design/build/test/security/publication tasks cannot wait: their filesystem and external effects need separate checkpoint semantics.

`tasks.wait` accepts `id` (run), `leaseToken`, `requestMessageId`, `reason` (1–2000 bytes), `summary` (1–8000 bytes), `timeoutSeconds` (integer 60–86400), `steps` (integer 1–23), and `source` (`repositoryId`, `commit`, both empty or an existing exact project commit). The Worker journals this operation before submission, using the existing durable completion mechanism. An exact replay returns the same wait, including after wake-up; conflicting replays fail. Current credential/Worker authority is always rechecked.

One transaction creates a `message_wait`, marks the run `suspended`, deletes its lease, and puts its task in `waiting_message`. The record binds project/goal/requirement/approved plan/task recipe/source/request/original run and an untrusted summary. Waiting consumes no running lease or execution reservation. No model conversation or tool output is persisted or replayed.

Only `result`, `clarification` or `review` from the original recipient, addressed to the original requester and exact waiting task, with `causationId` equal to the original request, can wake it. Worker replies must also come from the requested recipient task. `progress`, `cancel` and unrelated messages cannot wake or authorize anything. The reply is atomically recorded in the task inbox under its existing 32-message/64-KiB limits before the task becomes pending. A replacement requester Worker can consume that receipt even if the original Worker has been disabled. This exception applies only to an exact active wait reply; ordinary Worker-addressed messages retain their existing routing.

A reply received before wait registration is checked during the same suspension transaction. New replies trigger the same matching path. Concurrent wait/reply/claim operations cannot lose or duplicate a wake-up. The first eligible reply wins; later replies are ordinary messages. A resumed run is fresh inference with approved current context, the frozen read source, the untrusted summary and durable inbox. It never automatically replays a command.

## Limits and invalidation

At most three waits per task; at most 24 steps across a suspension chain. Resumptions do not consume failure retries, but actual failed/expired runs retain existing bounded attempts. A task that has waited cannot exceed its original cumulative model budget or active execution time: claim subtracts previous run expenses and active intervals, and refuses unresolved reserved charges. Goal budget checks still apply. No automatic budget expansion or approval substitution.

The task recipe (including dependencies, instructions, role/runtime, declared source and frozen design reference) is hashed without mutable execution counters. Resume must match it, the current approved requirement and active approved plan. Requirement/plan changes and cancellation close waits in the same mutation. The control-plane 15-second maintenance loop, Worker polling and incoming replies expire overdue waits or detect invalid bindings. Expiration blocks the task with an explicit reason; only human retry/replanning can continue it. A late reply never revives an expired/cancelled wait.

## Workbench and interfaces

REST `commands/tasks.wait` and MCP `platform_command` share the governed command. Readable `message_wait` objects appear through the authorized workspace/resource APIs without leases. The work-item dialog displays waiting status, request, recipient, deadline, summary, wake reply and suspension history. The addressed human collaborator can reply in that dialog; owners cannot impersonate another recipient. Replies are coordination input, never requirement/plan/release approval.

## Verification

Meaningful tests cover SQLite and PostgreSQL concurrent wait/reply/claim, early/late replies, idempotent lost responses, rollback, exact sender/task/causality matching, disabled requester migration, timeout, source/recipe/requirement/plan changes, protected internal fields, cumulative budgets/time/steps, capacity and credential boundaries. A real Worker/HTTP/local deterministic provider flow must suspend, free the node, execute the responder task and resume without human retry; no paid models. Real database backup/restore and an independent archive/SQL reader verify durable state. Desktop/mobile browser flows verify reply, permissions, errors, keyboard/dialog and overflow behavior. Existing preview/index/outputs/Docker/AVD remain unchanged.
