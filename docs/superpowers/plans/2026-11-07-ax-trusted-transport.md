# AX Trusted Transport Implementation Plan

> For agentic workers: use superpowers:executing-plans sequentially. User instructions prohibit subagent dispatch.

**Goal:** Bind AX API and guest execution to explicit authenticated endpoints while preserving durable ownership and fail-closed recovery.

**Architecture:** Administrator TLS configuration is loaded into an immutable per-command session. Direct upstream protobuf clients replace inherited CLI routing. Existing sandbox ownership stores public transport identity and retains its recovery gates.

**Tech Stack:** Go 1.27.1+, crypto/tls/x509, pinned AX and Substrate env protobuf, gRPC, SQLite/PostgreSQL, existing React workbench.

**Spec:** ../specs/2026-11-07-ax-trusted-transport-design.md

## Global constraints

- Main thread only; preserve 261 index entries, unborn branch, original preview and unrelated state.
- No paid model, real cluster/store/production mutations, or accepting Apple agreements.
- No private key/certificate bodies or local credential paths in platform records or logs.
- Both endpoints require TLS 1.3, CA validation, DNS name, SPKI pin and client certificate; no CLI/direct/plaintext fallback.
- Original product/P3 acceptance remains open.

## Tasks

- [x] 1. Write and observe a failing test: an AX configuration without secure identity/explicit legacy consent must not invoke even a local CLI. Add TLS session loading and real handshake tests rejecting malformed endpoints, CA/name/SPKI/client mismatches, insecure files and mixed configuration. Verify snapshots resist material/environment drift.
- [x] 2. Implement direct AX control and guest protocols in focused transport/control/guest files. Exercise exact workspace/task creation, Running/debug/Actor binding, actual command output/nonzero exit, EOF/cancel/writer failure and no WorkerIP use against actual local gRPC services. Use no provider-returned state as independent quality evidence.
- [x] 3. Bind public transport identity in sandboxes.prepare; validate exact schema, strings and hashes; preserve legacy rows. Run HTTP Worker, PostgreSQL contention and archive/restore with no private materials persisted. Test uncertain creation/cleanup through the existing recovery barrier.
- [x] 4. Show endpoint/name/pins in existing SandboxRecovery, add affected browser coverage, migrate the sample and API/Worker/operations documentation. Verify desktop/mobile rendering without replacing the original preview.
- [x] 5. Run full Go race with a dedicated temporary PostgreSQL, vet/build/tidy/diff-check, native and Linux/Windows compilation, affected browser flows and standalone protocol/backup readback. Retain initial failures. Export four new deliverables and independently read all source bytes/modes, evidence and document links. Close only this increment after verification.

## Validation commands

```sh
rtk proxy go test -race -count=1 ./internal/integrations -run 'TestAX'
rtk proxy go test -race -count=1 ./internal/platform ./internal/worker -run 'Test(Sandbox|PostgresSandbox|WorkerAX)'
rtk proxy python3 work/run-ax-trusted-transport-go.py --full
```

The private harness creates its own PostgreSQL and preserves raw JSON test events. Test counts come from those events, never estimates. Full tests run once after implementation; later repetition requires a new code change or unresolved failure. Completion requires standalone inspection of deliverables and preserved original state, not this checklist alone.
