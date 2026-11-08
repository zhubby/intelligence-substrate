# Release Feedback Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in the main thread. User AGENTS.md prohibits subagents.

**Goal:** Keep accurate, late production feedback usable after a requirements revision without reopening release gates.

**Architecture:** An explicit bounded upload derives an immutable report source from an already published candidate package. Transactional commands register reports and production observations; existing read models and workbench expose history with current authority.

**Tech Stack:** Go, SQLite/PostgreSQL, REST, React/TypeScript, Playwright.

**Spec:** docs/superpowers/specs/2026-11-07-release-feedback-design.md

## Global Constraints

Preserve546 source paths,257 outputs,261 index entries, zero commits and existing preview/Docker/AVD. No staging/push, paid inference, external channel/production mutation or license acceptance. Use rtk for shell commands. New private evidence under work/ and new deliverables in the approved outputs directory.

### Task 1: Exact-source bounded reports

Create internal/platform/release_feedback.go, artifact_upload.go and release_feedback_test.go; modify releases.go and service.go. Expose `UploadReleaseReport(context.Context, Actor, M, io.Reader) (Resource,error)` and human-only `reports.attach`.

- [x] Write and run failing tests using newMetricFixture/real Git for old-version reports, immutable source, replay/content conflict, unpublished/wrong package, revoked authority, stream-time revocation, audit rollback and size/error cleanup. Example: revise the goal, upload `M{"releaseId":r.ID,"artifactId":pkg.ID,"name":"issue.txt","key":"ticket-1"}`, assert original revision and unchanged candidate hash.
- [x] Extract existing private CAS write into `uploadContent(io.Reader,int64) (string,int64,error)` without changing ordinary package/prototype limits. Implement preflight and commit checks, published-target source validation, canonical key input, VerifyArtifact and transactional Remember/Audit. Run `rtk proxy go test -race -count=1 -run ReleaseFeedback ./internal/platform`.

### Task 2: Late measurements and protected report purpose

Modify delivery_measurements.go, delivery_measurement_views.go, quality_reports.go, releases.go, release_materials.go and runtime.go. Add focused tests in release_feedback_test.go and a dedicated PostgreSQL concurrency/restore test.

- [x] First fail tests where `production_defect` explicitly selects the released old revision, a different/unpublished target is rejected, and feedback reports cannot become business acceptance, manual independent evidence, store metadata or runtime proof.
- [x] Validate optional measurement revision against platform-derived source revision, recheck bound feedback reports on read, and keep all non-defect measurements current-only. Verify historical/current metric counts, correction history and integrity-loss exclusion. Run focused race suites, then a fresh dedicated PostgreSQL test using two connections and actual backup/restore.

### Task 3: HTTP and usable historical entry

Create internal/httpapi/release_feedback.go/test.go and web/src/ReleaseFeedback.tsx; modify server.go and DeliveryMetrics.tsx. Add web/tests/release-feedback.spec.ts, reusing exported synthetic metricFixture/openMetrics helpers.

- [x] Write failing HTTP tests for strict singleton query, CSRF, anonymous/Agent rejection, exact downloaded bytes and stale-scope registration. Implement the bounded route and source-derived upload.
- [x] Add failing browser flows: publish a candidate through governed test callbacks, change requirements/remove its target, choose historical scope, upload a new local file, record a late defect and observe old=1/current=0. Cover same-key network retry, invalid candidate, missing attachment, Escape/focus, keyboard and390px overflow. Implement only the existing-workbench entry and run `rtk proxy python3 scripts/verify-workbench.py --suite release-feedback --suite delivery-metrics --work-dir work/<new>/browser-focused`.

### Task 4: Verification and delivery

- [x] Main-thread correctness/security/simplicity review; run full race/vet/build/module diff with isolated PostgreSQL, all relevant browser suites and formats. Read actual tar/SQLite/report bytes independently and stop owned services.
- [x] Update API, operations, README, validation and a dedicated runbook. Verify baseline preservation, export source/report/runbook/evidence/screenshots, read back manifests and metadata, then complete024 and refresh/read back the final package. Overall platform goal remains active until real P3 conditions are met.

## Verification outcome

Full Go race530 top-level/1867 with subtests and full browser20 files/40 groups/80 flows passed with no failures, skips or browser retries. Dedicated PostgreSQL and local providers stopped. Independent tar/SQLite/Git readback verified18 unchanged resources,2 replay keys and historical1/current0. Initial six-deliverable ZIP/JSON/document/PNG/hash readback passed; this completion metadata is included in the refreshed final package and checked again.

The source/index/outputs/original preview/AVD preservation checks passed. Shared Docker baseline has disclosed lifecycle differences: cyberos-backend-review-validation exited and was destroyed; cybervisual-network-camera-test:latest moved to another image while the original ID remains. The increment performed no Docker mutations. All other37 baseline containers,18 image IDs,23/24 image references and17 volumes were observed intact. This is not a claim that every shared resource stayed unchanged. Apple SDK probes remain69; overall P3 remains open.
