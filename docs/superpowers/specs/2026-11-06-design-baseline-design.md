# Versioned design and prototype baseline

This is a P2 increment of the approved Intelligence Substrate product, not completion of the original P3 objective.

## Problem and chosen behavior

Prototype artifacts can already be previewed in a network-disabled HTML sandbox, but no first-class design record ties them to reviewed decisions or the approved execution plan. Agents currently receive knowledge, not an authoritative frozen design.

Add immutable `design` proposals with a goal-local version number, exact requirement revision, title, summary, decision text, 1–100 existing criterion IDs and 0–6 prototype references. Each reference records artifact ID, SHA-256, size, repository, full source commit and declared target. Prototype bytes must exist, verify and fit the existing 2 MiB preview boundary. Decisions cannot amend requirements or quality policy. Every new proposal needs a reason; later proposals do not mutate earlier versions.

Humans and project-scoped product/architect/developer/design agents may propose through the command API and MCP. A leased design worker may publish its structured result and optional local HTML prototype when finishing a design task. Its controller supplies scope, lease and source metadata; it cannot approve its result. Other worker task kinds cannot propose designs or gain prototype-only permissions to upload packages.

## Approval and authority

Design finalization happens in the existing human architect `plans.approve` decision. Do not introduce another mandatory approval category. A plan may select a design using `designId` plus `expectedDesignBinding`; the server embeds the exact proposal snapshot in the plan binding. Planning without a new selection inherits the current plan's frozen design, if any, so omission cannot silently discard an approved decision. A new requirement revision starts a new design basis.

The manual and model planning interfaces both support explicit selection. Model inference receives the exact selected snapshot, and completion rechecks context before producing an unapproved plan. The model cannot invent or substitute a design selection. Selected record drift rejects completion; unrelated unselected proposals do not. No external or paid models are used for acceptance.

At approval, recheck requirement version, proposal binding, frozen metadata and actual prototype bytes. Save the same frozen design and digest in the plan approval atomically with task creation. The source of execution authority is that approval, not the live design row. A missing, malformed or mismatched approval fails closed before an agent model call. Legacy plans with no design remain supported.

Replacement is a new plan proposal and human approval. Pending proposals alone do not cancel work. The existing plan replacement invalidates old tasks/runs, quality policies and release candidates; existing evidence remains historical and cannot bypass the new plan's quality gates. Requirement change also marks design records stale. Historical design, prototype and approval records remain readable under project permissions.

## Product interface

Extend the existing delivery-plan page with design records, criteria, exact prototype preview links and a proposal form. Mark designs as waiting, selected in the active plan or historical using approval/plan context. Show the frozen design inside every plan and its approval confirmation. Keep editable content escaped; prototype execution stays in the existing sandbox.

Support desktop and 390-pixel mobile layouts, keyboard entry, Escape and focus restoration, loading/failure/retry with one captured idempotency key, requirement changes while a form is open, viewer restrictions and no horizontal overflow.

Review correction: a CSP on a standalone prototype did not prevent self-navigation. Render escaped prototype bytes in a sandboxed srcdoc child with an outer frame-src policy that prevents navigational HTTP requests. Preserve internal interaction and the original artifact bytes, and verify the browser behavior against a local navigation probe. Browser policy does not replace enterprise network egress isolation.

## Acceptance and boundaries

Prove real Git/prototype registration, immutable versions, payload-conflicting idempotency, role/project/lease constraints, exact plan binding, byte corruption rejection, frozen worker context, model selection/drift, replacement/requirement invalidation, PostgreSQL concurrency and backup/restore. Browser flows use isolated builds/state on 18082 and local deterministic providers only. Preserve all 498 source files, 233 older outputs, 261 index entries, zero commits, original 18080 preview, Docker resources and AVD files.

Do not stage, commit, push, deploy the original preview, accept Apple agreements, call paid models or operate real stores/production. Apple remains externally blocked until the user's local Xcode acceptance takes effect. Full P3, actual channels, physical devices and native Windows/Apple execution remain open.
