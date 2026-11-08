# Windows MSIX Signing Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans in the main thread. User instructions require sequential work without subagents, commits, pushes or staging changes.

**Goal:** Produce an exact approved Windows MSIX through a dedicated once-only signing service, then independently verify the final artifact.

**Architecture:** Reuse authoritative signingInput and isolated Worker dispatch. A shared bounded MSIX parser connects the Flutter build helper to an administrator Python signer and fixed PowerShell certificate/ACL inspection. Existing publication and native quality gates remain authoritative.

**Tech Stack:** Go/SQLite/PostgreSQL, Python3.11+, Windows SDK MakeAppx/SignTool, Windows PowerShell5.1, Flutter x64, React/Playwright.

**Spec:** ../specs/2026-11-07-windows-signing-design.md

## Global constraints

- Preserve full product/P3 acceptance. Synthetic signature providers cannot prove Windows signing, installation, updating or release.
- Main thread only; no commits, pushes, staging changes, paid models, actual stores/production or acceptance of Apple agreements.
- Preserve734 source files' unrelated bytes,335 historical outputs,261 index entries/zero commits,18080/PID36063/dist and current Docker/AVD/keychain.
- Do not import certificates, change trust or keychain, install a VM or modify existing runtime services. Use work for intermediate data and existing outputs for deliverables.

## Task 1: Approved identity and dedicated Worker

**Files:** internal/platform/android_signing.go, windows_signing.go, windows_signing_test.go, windows_signing_postgres_test.go; internal/worker/android_signing.go, runner.go, windows_signing.go, windows_signing_test.go.

**Interface:** `signingMode=windows-msix` binds `expectedSigner`, `expectedWindowsPublisher`, `expectedWindowsPackage`, `expectedWindowsVersion`. Versioned request adds exact task/plan/source/input identity; receipt includes certificateSHA256, certificateSHA1, publisher, packageName, version, architecture and exact final bytes.

```json
{"signingMode":"windows-msix","runtime":"windows","role":"release","kind":"build","target":"windows","inputArtifactFrom":"unsigned","dependsOn":["unsigned"],"expectedSigner":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expectedWindowsPublisher":"CN=Company","expectedWindowsPackage":"com.company.feedback","expectedWindowsVersion":"1.0.0.0"}
```

- [x] Write and observe failing plan/exact-input/QA tests; reject Linux/darwin, repository commands and missing/foreign identity.
- [x] Implement authoritative binding and exact result validation; only final signed bytes schedule QA; CI release remains rejected.
- [x] Write Worker failure tests before routing windows-1; exact download/receipt/upload and dedicated-process cancellation.
- [x] Verify dual-store PostgreSQL claim/completion contention and accurate archive/restore without repeated signature.

## Task 2: MSIX parser and native build

**Files:** scripts/windows_msix.py, test_windows_msix.py, build-reference-client.py, test_build_reference_client.py.

**Interfaces:** `inspect(path, signed=False)` returns identity/version/publisher/architecture, member digests and payloadDigest; `create_layout(...)` generates a fixed single-entry full-trust manifest. Build target `windows-unsigned-msix` uses explicit MakeAppx/publisher/package/version/logo and rejects non-Windows/HTTP/preexisting outputs.

```python
metadata = inspect(unsigned_path, signed=False)
assert metadata['architecture'] == 'x64'
assert metadata['version'] == '1.0.0.0'
```

- [x] Observe parser and build-target failures with actual ZIP/XML/PE fixtures and native-host rejection.
- [x] Implement bounded ZIP/Windows paths/XML/identity/full-trust/BlockMap/PE checks and controlled layout; reject malformed/unsigned-state ambiguity.
- [x] Build Flutter windows release x64, MakeAppx without skip-validation, inspect exact MSIX and emit truthful source/SDK/artifact manifest.

## Task 3: Private signing service

**Files:** scripts/windows_signer.py, sign-windows-msix.py, windows-signing-tools.ps1, test_windows_signer.py; deploy/signer.windows.example.json and worker.signer.windows.example.json.

**Interfaces:** `Signer(config).handle(request)` accepts strict windows-1; `--inspect OPERATION_KEY` is read-only. Config fixes certificate DER/store/tools/timestamp and scope/identity; only stateDir paths are service-generated. Intent precedes SignTool and successful receipt follows exact readback/fsync.

```python
stable = {k:v for k,v in request.items() if k not in {'runId','artifactPath'}}
# The operation journal rejects any new binding for an existing operationKey.
```

- [x] Observe missing-signer failures, then cover real SQLite/files/ZIP/subprocesses with only the Windows tools replaced.
- [x] Implement strict configuration/request, DACL and certificate inspection, bounded private logging and fixed SignTool sign/verify plus Authenticode/timestamp readback.
- [x] Compare every non-signature member; retain raw input; durable intent, read-only replay and unknown-result blocking, cancellation/child cleanup.
- [x] Run actual non-Windows CLI negative checks; record lack of enterprise native success explicitly.

## Task 4: Workbench and final acceptance

**Files:** web/src/pages/Delivery.tsx, web/tests/windows-signing.spec.ts; docs/windows-signing.md, docs/api.md, docs/workers.md, docs/releases.md, docs/validation.md, README.md and main ledger.

- [x] Browser red before showing approved Windows publisher/package/version and awaiting-reconciliation/native-verification state; desktop/mobile render review.
- [x] Full Go race with isolated PostgreSQL; Python tests; Go vet/build/tidy/gofmt; TypeScript/isolated Vite, relevant browser flows and cross-compile Windows Worker.
- [x] Standalone raw store/backup/ZIP/journal/hash audit; verify preservation and terminate only task-owned temporary processes.
- [x] Export four sanitized deliverables; independently read back source bytes/modes, raw test evidence and links. Close040 local implementation only after output acceptance, then re-export bookkeeping; fullP3 remains open.

## Verification notes
- Final full Go race: 694/2423, 36 PostgreSQL; Python166, Chrome24, ten viewed images.
- PostgreSQL first-schema catalog race was reproduced and serialized; CLI/catalog/Docker helper dependencies were repaired after real red portability checks.
- Final raw store/audit/backup/private-journal/MSIX readback passed. Successful Windows native tools remain synthetic; real native installation/updating and complete P3 remain open.

- Initial output readback passed; only040 local implementation is closed. Completion bookkeeping is included in the final re-export/readback.
