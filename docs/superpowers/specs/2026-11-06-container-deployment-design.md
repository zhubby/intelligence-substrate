# Production container deployment verification

## Goal and acceptance boundary

Continue the approved enterprise delivery plan by making the control-plane container deployment reproducible and exercising its operational recovery. This is a local, isolated production-configuration drill. It does not authorize an enterprise rollout, constitute high availability, or complete P3.

## Grounded findings

- The unmodified multistage Dockerfile fails before build execution: the configured Docker Hub mirror's token endpoint returns HTTP 400 `invalid client ID` for Node, Go and Debian. A direct HTTPS mirror resolves the same images without changing daemon configuration. Actual image build and runtime acceptance remain to be recorded.
- Dockerfile already accepts three base-image arguments, but Compose cannot pass them or select an already-built platform image/database mirror.
- The container is intended to run as uid/gid 10001, read-only, with dropped capabilities, private PostgreSQL networking and persistent `/data`. Existing host-process tests do not prove those container constraints work.
- The production service requires HTTPS and Secure cookies. A loopback HTTP health check alone cannot prove usable production authentication.
- Logical backup intentionally omits credentials. The drill must prove restored identity, project history, Git and artifacts, and reject pre-restore credentials and damaged archives.

## Design

1. Keep the existing multistage Dockerfile. Expose explicit Compose image/build-argument overrides with unchanged upstream defaults; do not silently select a third-party mirror. An operator can pin trusted enterprise images by digest. The first mirror build additionally reproduced a Go dependency timeout at `proxy.golang.org`; expose non-secret Go/npm dependency mirror arguments too, retaining go.sum/npm integrity and upstream defaults.
2. Add an opt-in standard-library Python deployment verifier consuming an already-built platform image and already-pulled PostgreSQL 17 image. It creates a new private directory, random Compose project, loopback ports and throwaway secrets. It refuses an existing work directory and never uses the repository's `.env`, data or secrets. It does not build, pull, prune, change Docker configuration, or call models/stores.
3. Use the actual `compose.yaml`, with a private override only for the temporary image/configuration. Launch the unchanged service in production mode behind a task-owned loopback TLS reverse proxy. The client trusts only the generated local certificate; it does not disable TLS validation. The proxy has one fixed upstream and is not an open proxy.
4. Exercise real HTTPS authentication and origin/CSRF rejection, bundled frontend asset serving, real API repository commits and Git smart-HTTP clone/push/protected-main rejection, content-addressed artifact upload/readback, and requirement approval history. These are synthetic fixture decisions within the isolated drill.
5. Stop and restart database/platform using their volumes; verify identities, sessions, token, source, artifacts and audit survive. Stop platform writes before logical backup. Verify backup integrity, damage an artifact in a private archive, and prove the failed restore rolls back database import and removes its new directory. Restore the intact backup into a new database and new volume; verify source/artifacts/history/audit and credential revocation, then require fresh login/token issuance.
6. Always attempt cleanup of only the generated project's containers/network/volumes, including on failure. Preserve the private fixture/config/backup for diagnosis. Export a redacted JSON result and command log with accurate completed checkpoints, failure details and cleanup status. Never export passwords, cookie/token values, SQL DSNs, TLS private keys or raw identity backup.

## Verification and delivery

Unit regressions cover refusal of unsafe/existing paths, image input, secret redaction (including timeout/failure output), fixed-upstream TLS routing, and archive corruption targeting the real artifact. The opt-in integration drill validates actual Docker/Compose behavior and is added after image build in CI. No UI behavior changes are planned, so no unrelated browser/native/model regression is required.

Preserve 261 staged entries, 145 previous output files, the original 18080/PID36063 preview and all `web/dist` bytes, and unrelated Docker resources. Retain new source/report/runbook/evidence under new output names and independently read them back. Record image architecture, image ID, source hashes, actual commands, checkpoint count and limits; do not describe an overridden-image build as default Docker Hub access working.
