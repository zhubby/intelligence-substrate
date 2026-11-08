# Domestic Store Recovery Implementation Plan

> For agentic workers: execute sequentially with superpowers:executing-plans. User instructions prohibit subagents and incidental Git index changes.

**Goal:** Reconcile positively evidenced domestic-store writes without replaying a publication or bypassing delivery governance.

**Architecture:** A local administrator entry point validates exact approved identity and proof, then atomically records recovery, private evidence, immutable version ownership and the resulting journal snapshot. Pure observation helpers reuse the existing connector protocols.

**Tech Stack:** Go, database/sql, SQLite, existing HTTPS connectors and CLI.

**Spec:** ../specs/2026-11-06-store-recovery-design.md

## Global constraints

- Approved platform scope; no additional approval ceremony for implementation. No real channel writes, paid model calls or Apple agreement acceptance.
- Main thread only. Shell/subprocess commands start with `rtk`. Preserve index, existing outputs, preview and default build assets.
- No generic unlock/abandonment, no upstream writes in recovery, no weak publication assertions.

## Tasks

- [x] 1. Write failing SQLite/HTTPS recovery tests for all four channels, invalid binding/proof/stage, and safe API versus portal results. Run and retain the red result.
- [x] 2. Add administrator request/history/evidence types, durable SQL schema, exact snapshot transaction and idempotent replay. Keep private report/attachment bodies inside the publisher database. Guard reuse of recovered Huawei/Xiaomi versions.
- [x] 3. Add Huawei and OPPO observation recovery and Xiaomi/vivo portal verification. Preserve existing connector behavior and history through continuation. Add stale/concurrent observation, lock/version ownership and rollback tests; run focused race tests.
- [x] 4. Add exclusive `-reconcile-store` mode and strict administrator JSON; test CLI invocation, inspection, durable replay and Worker-mode rejection with a task-owned built binary.
- [x] 5. Review source and update operator runbook, channel docs and validation. Run Go vet/race with task-owned PostgreSQL and 21 cross-platform command builds; preserve raw logs and explicitly separate local contract evidence from external acceptance.
- [x] 6. Export a uniquely named source snapshot, report, runbook and evidence to outputs, then independently compare archive/source, log and binary hashes, prior outputs, staged entries and live preview. Mark only this increment complete; keep overall P3 active.

## Verification

`work/store-recovery-zanv0vgo/regression/regression-result.json`: Go vet/race 332 top-level / 1282 including subtests, no failures or test skips; recovery subset 17/212 included. Dedicated PostgreSQL17.6 at loopback53426 stopped. Seven commands build on three platforms (21 binaries); Linux/Windows binaries were not executed. Built CLI proof covers132 invocations and6 persisted fixture variants. Main-thread correctness/security review only; no independent reviewer or subagents. No UI/native/model change or production store write; Apple prerequisites still exit69.

Initial package readback passed before completing this checklist:354 source files,141 prior outputs,261 staged entries,21 binary hashes/formats,132 CLI logs and6 private evidence bodies, stopped PostgreSQL, and unchanged live preview. Final source/archive receipts after checklist completion are recorded separately in `work/store-recovery-zanv0vgo/independent-readback-result.json`. Only this increment is complete; overall P3 remains active.
