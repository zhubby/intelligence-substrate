# Reference Application Quality Reports Implementation Plan

> For agentic workers: use superpowers:executing-plans sequentially in the main thread. No subagents, staging, commits or push.

**Goal:** Produce artifact-bound, archived independent browser quality evidence for both actual reference applications through the automatic Worker flow.

**Architecture:** Human-controlled pipeline.reportPath determines report collection. The Worker uploads a report under its test lease; completion and release validate exact provenance. A single-application browser adapter exercises actual compiled Flutter bytes against the exact source backend.

**Tech Stack:** Go, SQLite/PostgreSQL, React/TypeScript, Python archive validation, Node/Playwright, current local Flutter/Go toolchains.

**Spec:** ../specs/2026-11-06-reference-quality-design.md

## Global Constraints

Preserve432 source baseline,185 prior outputs,261 staged entries/zero commits,18080/PID36063/backend/web/dist and current37 containers/23 image references/15 volumes. No paid models, Apple agreement acceptance, real stores/production or Docker mutation. Main thread only; original P3 remains open.

## Task1: Protected report collection and exact evidence

Files: internal/platform/engineering.go, tasks.go, releases.go; new quality_reports.go and quality_reports_test.go; internal/worker/runner.go and quality_reports_test.go.

Interfaces: `func (s *Service) validateQualityReport(t *Tx, a Actor, run, task Resource, result M, required bool) (Resource, error)`; Worker reads reportPath from the fetched protected pipeline only, uploads with its existing upload method and returns reportArtifactId.

- [x] Add a failing actual-tool test in which a protected strategy writes a report and the evidence must retain the uploaded bytes. Add missing/borrowed/wrong-commit/wrong-run report rejections and path validation.
- [x] Run focused tests and retain red results before runtime edits.
- [x] Validate and persist pipeline reportPath, collect real report after tools and require exact current-run bytes for passing evidence. Reject report content/provenance changes at release.
- [x] Verify failure reports, no-report compatibility, policy snapshots, lease/authority failures and archive restoration.

## Task2: Single-application reference browser verifier

Files: new scripts/verify-reference-web.mjs and scripts/reference-web-archive.py, tests/test_reference_web_archive.py or scripts/test_reference_web_archive.py; reusable existing scripts/verify-reference-apps.mjs checks remain the acceptance model.

Interfaces: CLI `--application PATH --api-url http://127.0.0.1:PORT --output PATH` consumes IS_ARTIFACT_PATH and writes an immutable report tar.gz containing result.json and screenshots. Unsafe archives fail before extraction or tools.

- [x] Add red archive tests for traversal, links, duplicate/case-colliding paths, special files, oversized/unexpected entries and false Flutter entry bytes.
- [x] Implement bounded safe extraction and per-mode actual browser/API tests with owned process/server cleanup, undeclared network rejection and exact package/source digests.
- [x] Run both new generated applications through real Git/build/automatic-QA Workers, inspect reports and screenshots, prove candidate readiness is bound to the same packages and commits.

## Task3: Workbench, regression and deliverables

Files: web/src/pages/Engineering.tsx, web/src/QualityScheduling.tsx, web/tests/quality-scheduling.spec.ts; README, docs/api.md, workers.md, quality-scheduling.md, validation.md and main progress ledger.

- [x] Add the optional protected report path form, exact report download links and visible missing-report failures using existing controls. Verify desktop/mobile, keyboard/Escape/focus and no overflow.
- [x] Run full Go race with dedicated PostgreSQL, Python security tests, TypeScript/build, source browser suite, format/links. Stop owned services and preserve all original state.
- [x] Review exact runtime/source/evidence in main thread; export source/report/runbook/evidence/screenshots, independently read back all contents/hashes, close only014/scoped plan. Keep overall P3 open.

## Completion evidence

Final actual applications: work/reference-quality-e3fc4yp1/actual-source-exact/result.json. Each complete139-file Git tree and every SHA256 matches its archived report; original report bytes match platform proof digests. Read-only SQL confirms each application's2runs/2artifacts/1evidence/1dispatch/1awaiting_approval release. Full Go race426/1542, Python45, standard workbench50 and final related8flows pass.444source files,8new deliverables and185prior outputs independently read back; completion metadata is refreshed and read back again. Main-thread review only, no independent reviewer or subagents. Existing preview/index/Docker resources preserved. Apple SDK checks remain69; native/device/signing/store/enterprise/live-model autonomy and overallP3 remain open.
