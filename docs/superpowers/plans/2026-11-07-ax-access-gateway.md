# AX Access Gateway Implementation Plan

> For agentic workers: use superpowers:executing-plans sequentially. User instructions prohibit subagents, staging or commits.

**Goal:** Deliver a separately deployable certificate-to-project authorization gateway for AX control and Guest execution.

**Architecture:** Strict private administrator policy gates typed upstream clients. TLS material loading is shared with the Worker; gateway policy generations atomically own cancellation of admitted calls. Native AX remains an explicitly confined loopback upstream.

**Tech Stack:** Go1.27.1+, TLS1.3/x509, pinned AX/Guest protobuf, gRPC, structured JSON audit.

**Spec:** ../specs/2026-11-07-ax-access-gateway-design.md

## Global constraints

- Main thread only; preserve807 original source files,359 outputs,261 staged entries, unborn feat/intelligence-platform,18080/PID36063/dist and unrelated runtime state.
- No paid model, real cluster/store/production mutations, Apple agreement acceptance or private material export.
- No remote plaintext/control fallback; native h2c allowed only for explicitly asserted private namespace and numeric loopback endpoint.
- Requests cannot bypass project/space/role/image/Actor policy. Response loss stays unknown; gateway never retries mutations.
- Full product/P3 remains open until actual declared multi-end/channel acceptance is proved.

## Tasks

- [x] 1. Record failing authorization tests using real peer certificates and independently fixed requests. Add strict policy validation/reload, deny-all on invalid/replayed revision, immutable snapshots, fail-closed audit admission and cancellation registration under one lock. Files: internal/axgateway/policy.go, policy_test.go.
- [x] 2. Extract the Worker's existing material/TLS loader into internal/trustedtls without weakening any045 transport rule. Add inbound server material validation and exact client chain handling. Verify all existing AX tests and fresh TLS boundary tests. Files: internal/trustedtls/client.go, server.go, tls_test.go; internal/integrations/ax_transport.go.
- [x] 3. Implement typed control/guest proxy with exact operation/resource/response checks and stripped outgoing metadata. Verify actual command exit and all denial cases against local upstream services. Files: internal/axgateway/control.go, guest.go, gateway.go and gateway_test.go. Default unknown methods return denied/unimplemented and never reach upstream.
- [x] 4. Add cmd/ax-gateway lifecycle, private configuration, separate TLS listeners, explicit upstream readiness, SIGHUP policy revocation, bounded shutdown and deployment sample/runbook. Run a separate-process TLS/gRPC/reload drill; preserve raw results and verify with an independent client. Files: cmd/ax-gateway/main.go, internal/axgateway/config.go, deploy/ax-gateway.example.json, deploy/ax-gateway-policy.example.json, docs/ax-access-gateway.md; README/Worker/AX docs.
- [x] 5. Review scoped implementation in main thread; run final Go race with a dedicated PostgreSQL, vet/build/tidy/diff, Linux/Windows compilation and retained045 protocol regression. Reproduce executed binary from tested source, inspect original-state preservation, export four new deliverables and independently read every source byte/size/mode, raw evidence and links. Close only046 after those pass.

## Commands and expected boundaries

```sh
rtk proxy go test -race -count=1 ./internal/axgateway ./internal/trustedtls
rtk proxy go test -race -count=1 ./internal/integrations -run TestAX
rtk proxy go test -race -count=1 -timeout=20m ./...
```

The full run uses a private PostgreSQL fixture with raw JSON test events, counts successes/skips/failures directly, and stops its database. Standalone proof distinguishes actual gateway/TLS/command processes from local upstream protocol replacements. Policy reload does not prove raw upstream network closure, certificate issuance/enterprise revocation or physical Actor termination.

## Implementation findings

- AX ListTasks defaults to50. Three observed red cases (hidden retained Task, later-page error, list omission with retained exact lookup) now require bounded explicit100-item enumeration plus exact Task and Workspace NotFound. Complete inventory errors remain unresolved.
- Initial gateway red exposed CreateTask image/spec checks happening after mutation. Principal-dependent validation now runs inside admission before audit and forwarding, under the same lock as policy revision and cancellation registration. Invalid request tests assert no upstream mutation.
- Malformed immediate EXITED process results are rejected; cancelling a generation does not turn an upstream write into confirmed failure or physical process termination.
