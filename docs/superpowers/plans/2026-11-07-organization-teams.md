# Organization teams implementation plan

> For agentic workers: use superpowers:executing-plans sequentially in the main thread. User AGENTS.md prohibits subagents. Preserve the original staging, preview and unrelated work; no commit/push or external publication.

**Goal:** Govern enterprise team membership and project role bindings through the same live authority as all delivery operations.

**Architecture:** Global team/current and immutable revision resources, project-scoped grant resources, transactional replay/audit/revocation, and derived project roles. Existing direct-owner recovery anchor remains required.

**Tech stack:** Go1.27/database/sql SQLite/PostgreSQL; React/TypeScript; existing HTTP/CSRF and Playwright runner.

**Spec:** ../specs/2026-11-07-organization-teams-design.md

- [x] Add meaningful failing platform tests for inherited access, direct union, current identity/team/binding state, replay/revisions, exact credential scope and atomic rollback. Implement focused organization lifecycle and role derivation in separate files, integrating existing current-authority and workspace reads.
- [x] Add failing HTTP/current-session/CSRF/metadata-isolation and concurrent issuance tests. Route catalog/detail/commands through current authorization; verify no Agent/Worker/MCP privilege escalation and last-direct-owner recovery.
- [x] Add real PostgreSQL two-client mutation/issuance and backup/restore proof. Verify original group/binding/history/replay/audit state independently with Python/SQLite/tar, excluding restored credentials.
- [x] Add browser regression proving the missing team flow, then organization/binding forms and effective-role/source display. Verify desktop/390px, nonadmin inherited owner/developer roles, revocation, loading/errors, keyboard/Escape/focus and overflow.
- [x] Run final complete Go race with dedicated PostgreSQL, vet/build/tidy, TypeScript/Vite and full browser suites. Review source/state transitions in the main thread. Recheck source/index/outputs/preview/Docker/AVD preservation; export and independently read back six deliverables before marking026 complete. Full P3 stays open.

2026-11-07 completion: Full559/1981Go race with dedicated PostgreSQL, vet/build/tidy, static/cross-compile and22-file/44-group/88-flow browser checks passed. Standalone tar/SQLite verified12 resources,2 identities,18 audit rows,4 keys and0 restored credentials. First six-output ZIP/JSON/Markdown/PNG independent readback passed; complete026 and refresh the final source package. Original573 paths/269 outputs/261 index entries/preview/Docker/AVD preserved. Full P3 remains open.
