# Desktop Updater Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans sequentially in the current thread. The user's AGENTS instructions prohibit subagents. The approved product plan already authorizes Windows/macOS distribution and update capabilities; do not commit or push.

**Goal:** Check, stage and install a specifically approved desktop update using the existing signed feed, with durable recovery and independent evidence boundaries.

**Architecture:** A standalone Go CLI uses a focused updater package and SQLite state. A shared desktop verifier preserves publisher signature checks. Platform-native backends validate package metadata and invoke fixed native installation tools. No shell commands or trust configuration come from the feed.

**Tech Stack:** Existing Go 1.27.1+, SQLite, Ed25519/SHA256, HTTPS, macOS pkgutil/spctl/installer, Windows Authenticode/PowerShell/MSIX.

**Spec:** [desktop updater design](../specs/2026-11-06-desktop-updater-design.md).

## Global constraints

- Shell commands start with `rtk`; no subagents, commit/push, user installations, agreement acceptance or unrelated cleanup.
- Preserve the running 18080 preview, credentials, SDKs and prior deliverables.
- Fixed application/target/channel/origin/key/signer/package identity; HTTPS only, no redirects, bounded response sizes.
- Require explicit SHA256 consent before native installation; no retry of ambiguous installer execution.
- Existing feed protocol and publication/independent-quality gates remain authoritative.
- Distinguish TLS/SQLite/metadata tests, substituted native installation, real unsigned rejection, cross builds and real signed upgrades.

### Task 1: Trusted check and staging

**Files:** `internal/updater/client.go`, `internal/updater/state.go`, `internal/updater/client_test.go`.

**Interfaces:** `Open(Config) (*Client, error)`, `Close() error`, `Status(context.Context) (Snapshot, error)`, `Check(context.Context) (Snapshot, error)`, `Stage(context.Context) (Snapshot, error)`. Config fixes trust/destination; Snapshot records state, candidate feed, sequence, package path, installed version and attempt/error.

- [x] Write TLS/Ed25519/SQLite tests for restart-persistent sequence, renewal, signed conflicting payload, version rollback, foreign destinations, redirects and corrupt/oversized downloads. Native verification is an internal dependency substituted only in tests.
- [x] Observe missing updater behavior fail with `rtk proxy go test ./internal/updater` before implementing it.
- [x] Implement fixed profile validation, durable configuration binding/serialization, signed feed checks and bounded private staging; verify targeted race tests.

### Task 2: Native identity and at-most-once installation

**Files:** `internal/desktop/signature.go`, `internal/publisher/signatures.go`, `internal/updater/native.go`, `internal/updater/install.go`, native/install tests.

**Interfaces:** `desktop.Verify(ctx, target, filename, expectedSigner, artifact) (string, error)` preserves existing publisher behavior. Updater native dependency implements package verification, installed receipt lookup and installation. `Install(ctx, approvedDigest) (Snapshot, error)` and `Reconcile(ctx, confirmedStoppedAttempt) (Snapshot, error)` journal attempts; reconciliation requires operator confirmation of native termination for the exact attempt, and never dispatches an installer.

- [x] Write metadata tests for actual MSIX ZIP/XML identity/version, PKG PackageInfo/receipt parsing, tampering after staging and incorrect native signer; use real unsigned/native-platform rejection where available.
- [x] Write two-connection concurrency and restart tests proving one installer dispatch; an interrupted/failed installer must stay uncertain, and reading an exact installed receipt must resolve without executing again after stopped-attempt confirmation.
- [x] Extract the existing native signature verifier, implement fixed native commands and durable reservation/completion, then verify updater/publisher/distribution race tests.

### Task 3: Runnable CLI and evidence

**Files:** `cmd/updater/main.go`, `cmd/updater/main_test.go`, `deploy/updater.macos.example.json`, `deploy/updater.windows.example.json`, `docs/desktop-updates.md`, `docs/releases.md`, `docs/validation.md`, README, Makefile and CI.

**Interfaces:** `is-updater -config PROFILE -action check|stage|install|status|reconcile [-approve-sha256 DIGEST] [-confirm-stopped-attempt ATTEMPT]` emits structured state to stdout, errors to stderr and nonzero exit on failed/uncertain actions. Stage/check never install. Only installation accepts SHA256 consent; only reconciliation accepts stopped-attempt confirmation.

- [x] Write CLI tests rejecting absent/wrong approval, unknown configuration and actions before creating state or contacting a destination.
- [x] Add the command, explicit example profiles and operational guidance, including private state/ACLs, failed-install reconciliation and OS receipt limitations.
- [x] Run actual CLI against a dedicated local TLS feed and real unsigned package; record verified check/persisted floor and native rejection without installation.
- [x] Run appropriate Go vet/race and native/cross builds; validate docs, config and whitespace. Preserve the preview and save a Chinese report/evidence/source deliverable under the authorized outputs directory.

## Completion boundary

This increment does not complete real signed Windows/macOS upgrades, Flutter desktop packaging/integration or independent desktop business acceptance. Those require actual native nodes and signed applications. Original two-app, all-target/channel P3 remains incomplete.

## Verification ledger

- `work/updater-final-go-tests.jsonl` and `work/updater-final-go-summary.json`: 116 top-level tests, 180 including subtests, all passed; no skipped tests. Actual isolated PostgreSQL 17.6 was included and stopped after testing (`work/updater-postgres.json`). Go vet passed.
- `work/updater-builds.json`: Go 1.27.3 macOS arm64, Windows amd64/arm64 and Linux amd64 builds. Linux is a compile check, not a supported native update target. All new binaries lack formal distribution signing.
- `work/updater-cli-3313932757/result.json`: actual current macOS binary, TLS, distribution service handler, feed renewal, durable sequence replay rejection, real native unsigned-package rejection and missing-consent refusal. No installer executed; publisher native attestation is synthetic.
- Main-thread correctness/security/simplicity audit is recorded in `work/updater-source-review.json`; this is not an independent human review. Windows same-name publisher isolation, private Unix state, authoritative failure snapshots and explicit stopped-attempt reconciliation were corrected with failing/passing tests.
- Source links, JSON, Python AST, CI YAML and whitespace checked. The 18080 preview was not restarted, and prior reference-client deliverables were preserved. No main-repository commit/push or remote CI.
