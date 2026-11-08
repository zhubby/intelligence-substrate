# Delivery Metrics Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans task by task in the main thread. User AGENTS.md requires sequential work and no subagents.

**Goal:** Add trustworthy, version-bound delivery measurements and an honest workbench report.

**Architecture:** Human observations use immutable source bindings with governed void/correction history; channel transitions collect server observations. A typed read model reports sampled metrics and unknown coverage without changing existing delivery gates.

**Tech Stack:** Go, transactional SQLite/PostgreSQL, REST/MCP, React/TypeScript, Playwright.

**Spec:** docs/superpowers/specs/2026-11-07-delivery-metrics-design.md

## Global Constraints

Preserve all 532 existing source paths, 251 prior outputs, original 261 index entries, unborn branch, preview PID36063/static bytes, Docker and AVD. No staging/commits/push, paid inference, external channel or production mutations, or SDK license acceptance. All shell commands use rtk. Private evidence under work/. New user-facing outputs under the approved outputs directory only.

## Tasks

- [x] 1. Measurement command contract: failing tests for human/current-role authority, exact proof and version scope, pass/fail attempts, interval overlaps, replay, duplicate reference, void/correction and audit rollback. Implement internal/platform/delivery_measurements.go with focused helpers; wire service.go. Run focused race tests.
- [x] 2. Channel observations and metric read model: tests for first-preview origin, first-pass denominator, no-data nulls, historical scope, report corruption, execution denominators and interval accounting. Implement internal/platform/delivery_metrics.go and channel observation helper; initialize/append observed histories in releases.go. Run focused race tests and PostgreSQL concurrency tests.
- [x] 3. REST/MCP: failing tests for strict query parameters, authentication, CSRF on writes, revoked membership, foreign project/version and readonly Agent parity. Add handler/tool; preserve human-only writes. Run HTTP tests.
- [x] 4. Workbench: add web/src/DeliveryMetrics.tsx and governance tab. Write Playwright flows exercising records, void, missing data, goal/revision switches, loading/errors, keyboard, mobile and integrity failures. Run focused browser runner with task-owned state on18082; preserve18080.
- [x] 5. Validation and delivery: document API/definitions/limitations; main-thread correctness/security/simplicity review; full Go race with isolated PostgreSQL, vet/build/module diff, relevant browser regression. Read actual SQL/backup independently; stop and verify owned listeners. Verify preserved bytes and export new source/report/runbook/evidence/screenshots; independently read back hashes and completed todo metadata. Leave overall platform goal active.

**Completion evidence (2026-11-07):** work/delivery-metrics-l9ono6tc holds 522 top-level / 1,841 Go test passes, 19 browser files / 38 isolated groups / 76 flows, independent persisted-state proof, preservation, export and standalone readback results. No paid models, subagents, remote CI or preview update. The exact-source final export follows the completed metadata readback; overall P3 remains open and Apple SDK license probes still exit69.

## Validation commands

Use rtk proxy go test -race -count=1 -run 'DeliveryMetric|DeliveryMeasurement' ./internal/platform ./internal/httpapi. Full tests use work/pg-runtime/runtime/bin and a fresh private cluster via IS_TEST_POSTGRES_URL. Browser runner: rtk proxy python3 scripts/verify-workbench.py --suite delivery-metrics --work-dir work/<new-root>/browser-focused. Output paths never overwrite prior increment evidence.
