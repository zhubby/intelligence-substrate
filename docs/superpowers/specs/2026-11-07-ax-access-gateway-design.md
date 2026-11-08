# AX project authorization gateway

This continues the approved product's control-plane access isolation, business authorization and independent execution boundaries. It does not change the P3 acceptance criteria or authorize real cluster mutations.

## Problem and selected design

The Worker pins two TLS identities but no deployable component enforces the certificate's project/atespace scope at AX's native API. AX's native API is h2c; atespace itself is not a tenant boundary. A TLS terminator alone therefore cannot provide project authorization.

Implement a dedicated typed gateway. Alternatives considered: leaving authorization entirely to an unspecified external appliance would leave this product capability missing; modifying/forking upstream AX would couple business policy to upstream controllers. The selected gateway retains pinned upstream protobuf and denies every method it does not explicitly expose.

## Administrative trust and transport

- Separate control and guest TLS listeners, minimum TLS1.3 and h2, verify clients against an administrator CA. Configuration and material files must be private-node regular files with bounded sizes; private keys must deny group/other access.
- Control upstream is an explicit IP loopback host:port with an administrator assertion of a private network namespace. No DNS, environment routing, proxy, remote plaintext, automatic fallback or Kubernetes credential lookup. The native AX server must listen on that loopback in the same trusted pod/network namespace; host networking and unrelated workloads sharing it are unsupported. This topology is a deployment prerequisite, not something the gateway can prove.
- Guest upstream uses explicit TLS1.3/h2 CA/name/SPKI/client credentials through the same trust loader as the Worker. Both upstream connections must be prepared before either listener serves application requests.
- Private materials never appear in logs or policy records. Logs contain method, outcome, policy revision, principal/project, exact resource and public client fingerprint; never command bodies, file bytes or headers. An audit write failure rejects admission before upstream mutation.

## Policy and request authorization

Strict version1 JSON policy has a monotonically increasing revision and certificate SHA256 principals. Each active principal binds exactly one project and one dedicated atespace, pinned allowed images, and independent control/guest permissions. Fingerprints are unique; a space cannot belong to two projects and a project cannot map to multiple spaces. Multiple Workers for a project may have different certificates in the same space. No wildcard names, certificates or methods. A removed principal is revoked, even on an existing TLS connection.

Only platform-form names `is-sandbox-` followed by24 lowercase hex are managed. Control exposes GetTask, ListTasks, CreateTask, DeleteTask, SuspendTask, ResumeTask, GetWorkspace, ListWorkspaces, UpdateWorkspace and DeleteWorkspace. Requests must name the principal's exact space; unscoped Task lists are rejected; default limit0 is normalized to100 and explicit pages accept only limit100 with offsets0–9900 in steps of100. The Worker enumerates these bounded pages and also requires exact GetTask/GetWorkspace NotFound. Native AX defaults to50, so its first page alone can never prove absence. Creation permits only the approved pinned image, debug readiness `/bin/true`, matching workspace at `/workspace`, CPU2/memory4Gi, no environment/config/request resources and empty workspace spec. Unknown protobuf fields are rejected on requests. Returned tasks/workspaces and all list members must preserve exact space/name and valid managed identities; malformed, duplicate, cross-space or mismatched responses fail closed rather than becoming cleanup proof.

Guest exposes StartProcess and StreamProcessOutput only. Exactly one `ate-target-actor` must match the principal's space and a managed name. Gateway independently reads that exact Task from control upstream and requires Running/debug plus same Actor, workspace and permitted image before forwarding a Guest request. Only the controlled Actor metadata is forwarded; credentials and user metadata are dropped. Command and stream remain untrusted process operations, never independent quality or approval.

## Reload, failure and recovery

Policy reload is explicit via SIGHUP. Each accepted revision atomically replaces a deep-frozen validated policy; all admitted calls from the previous generation are cancelled, including streams. Admission and cancellation registration share a lock. Invalid/missing policy or a non-increasing revision puts the gateway into deny-all and cancels active calls; a corrected higher revision can reopen it. A process restart can read the configured revision anew; file rollback prevention across restarts is an administrator/configuration-management responsibility, not a durable revocation claim.

Cancellation cannot retract an upstream mutation already executed. Lost or cancelled RPC responses remain uncertain and the platform's existing sandbox reconciliation barrier applies. No retry, compensation, deletion or recreation is performed by the gateway. Existing certificate issuance, network closure, private namespace enforcement, physical fencing and remote host-loss recovery remain deployment acceptance requirements.

## Evidence required

Real TLS clients and local pinned gRPC services must verify allowed lifecycle/actual command exit, certificate role/space/image restriction, metadata spoofing, unexpected methods/fields/responses, fail-closed audit, invalid/replayed policy reload, concurrent admission/reload and active stream cancellation. A standalone gateway process must demonstrate policy change on the same client connection and real child-command results without touching the real cluster. Full regression must retain the Worker transport and durable ownership tests. Exported source/evidence must exclude generated credentials and private data; original source/index/output/preview/Docker/AVD/keychains remain preserved.

This is deployable authorization implementation and local protocol evidence, not real enterprise deployment, signed native client, device/store/production or two-application P3 acceptance.
