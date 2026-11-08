# Workspace-backed waits implementation plan

Implement the approved workspace-wait design sequentially in the current feature checkout. Preserve unrelated work and the original preview. No paid inference, subagents, commit/push or external publication.

1. **Checkpoint authority and persistence.** Add failing archive/HTTP/authority/replay tests, then distinct bounded CAS upload/download, transactional bindings, backup integrity and denial audits. Keep credentials out of resource state.
2. **Wait and claim.** Extend code/design suspension with an exact checkpoint; validate current source/target and carry only trusted continuation metadata. Restore partial integration decisions only through this chain. Keep existing analysis behavior and limits.
3. **Worker recovery.** Add failing real Worker tests, then source selection/snapshot, exact retry, verified replacement-tree restore and tracked mode reconstruction. Preserve original commit bindings and prevent stopped-source publication during uncertain suspension.
4. **Workbench and documentation.** Show durable workspace metadata and source/integration origin in wait history, with accurate task-neutral wording. Verify desktop/mobile, keyboard/focus and no quality/approval promotion.
5. **Acceptance.** Run scoped and full regressions, PostgreSQL concurrent replay/restore, independent persisted archive/SQL/Git readback and preservation inventory. Export source/report/runbook/evidence/screenshots and read them back. Complete only this increment; full P3 remains open.

Each step uses meaningful failing tests before production changes. Review source and state transitions in the main thread, fix concrete findings, and run appropriate fresh checks before completion claims.

Accepted 2026-11-07: Steps1–5 passed. Final Go race544/1932, full browser82 plus final related10 flows, PostgreSQL concurrent replay/backup/restore, independent tar/SQLite/Git readback and six-deliverable readback succeeded. Original source boundaries,261 index entries,263 prior outputs,preview,Docker and AVD inventories were preserved. Only increment025 is complete; native/device/signing/store/production acceptance and overall P3 remain open. See docs/validation.md and025 completion record for exact evidence and limits.
