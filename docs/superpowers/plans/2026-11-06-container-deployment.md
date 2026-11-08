# Container deployment implementation plan

> For agentic workers: use superpowers:executing-plans sequentially in the main thread. The user's AGENTS instructions prohibit subagent dispatch. Existing approved scope authorizes execution; preserve the unborn branch and original index rather than committing.

**Goal:** Build the full platform image and provide a repeatable production-configuration container/recovery drill.

**Architecture:** The production Compose file remains authoritative. A standard-library Python verifier supplies isolated configuration, a fixed loopback TLS proxy, real HTTP/Git clients, backup/restore checks and project-scoped cleanup.

**Tech Stack:** Existing multistage Node/Go/Debian image, Docker Compose, PostgreSQL 17, Python 3, OpenSSL and Git.

**Spec:** ../specs/2026-11-06-container-deployment-design.md

## Global constraints

- No daemon/mirror/auth configuration changes, TLS bypass, paid models, store writes or production rollout.
- Never read the repository `.env`/secrets into the drill; use an exclusive private work directory and random project.
- Preserve 261 staged entries, 145 prior outputs, preview PID36063/18080 and `web/dist` bytes.
- Cleanup only the newly generated project; no global Docker cleanup.
- Local production-mode verification does not complete P3, native signing, devices or formal publication.

## Task 1: Full image and configurable deployment sources

**Files:** `compose.yaml`, `.env.example`, `docs/container-deployment.md`.

- [x] Capture source/output/index/preview/Docker resource baselines in `work/production-deploy-hr5wl5el/`.
- [x] Reproduce default build and identify the HTTP 400 mirror-token failure before changing code.
- [x] Build the same multistage Dockerfile with explicit HTTPS image arguments; retain full build logs and image metadata.
- [x] Expose `IS_PLATFORM_IMAGE`, `IS_POSTGRES_IMAGE`, `IS_NODE_IMAGE`, `IS_GO_IMAGE`, `IS_RUNTIME_IMAGE` in Compose using existing defaults. Parse the resulting Compose configuration in tests, without printing secrets. The dependency timeout also required explicit non-secret Go/npm mirror arguments; source sums/integrity remain enabled. Actual Compose build passed and its RootFS/runtime configuration matches the directly built image.

## Task 2: Safe operational verifier

**Files:** create `scripts/verify-deployment.py`, `scripts/test_verify_deployment.py`.

**Interface:** `python3 scripts/verify-deployment.py --work-dir NEW_DIRECTORY --platform-image IMAGE --postgres-image IMAGE`. The directory's parent must exist; the target must not. Images must already be cached. Exit zero only when all checks and cleanup pass. Write `result.json` and `commands.json` with redacted evidence; retain private fixture inputs separately.

- [x] Write focused failing regressions for exclusive work directories, input validation, command-output redaction, fixed-upstream TLS forwarding, and artifact-damaged backup generation. Run `rtk proxy python3 -m unittest discover -s scripts -p test_verify_deployment.py -v`; retain the missing implementation failure.
- [x] Implement `Runner.run`, safe image/path validation, generated private Compose configuration, TLS proxy and bounded readiness checks. Record failed as well as successful commands, without secrets or raw backup bodies. A reproduced launch-error case also verifies the failure record.
- [x] Implement the real drill: production isolation inspection → TLS/auth/CSRF/origin → static assets → project/baseline/repository/Git/artifact → persistent restart → quiesced backup → corrupt restore rollback → intact restore → old credential rejection/fresh authorization → scoped cleanup.
- [x] Run unit regressions, then actual integration with the full image. Fix only reproduced failures; retain red/green evidence. Final Python: 39 tests including 9 new safety cases. Final drill-03: 10 checkpoints/46 commands, full resources/audit snapshot comparison and complete cleanup. Independent backup readback verifies 8 Git objects, the artifact and executable protection hook; it is file verification, not independent code review.

## Task 3: Continuous verification and reviewable delivery

**Files:** `.github/workflows/ci.yml`, `README.md`, `docs/operations.md`, `docs/validation.md`, `docs/container-deployment.md`, original platform plan and todo 007.

- [x] Add the opt-in container drill after CI's full image build, using the cached PostgreSQL service image. Upload only redacted result/commands on failure; do not upload the work directory. Remote CI remains unrun in this unborn/unpushed repository.
- [x] Document mirror/image selection, running the drill, private diagnostic files, cleanup/failure semantics and exact acceptance limits.
- [x] Run Python regression discovery and required focused checks. Review correctness/security and compare actual checkpoints against the spec in the main thread. Prior source/output/index/preview and original Docker resources verified; Apple SDK probes still exit69.
- [x] Export uniquely named source/report/runbook/evidence; independently compare source bytes, old output hashes, staged entries, original preview and Docker-resource preservation. Initial readback passed for 360 source files/four outputs, with 145 previous outputs, 261 staged entries, 32 original containers, 20 image references and 15 volumes preserved. Refresh only this task's four outputs after completing bookkeeping, then retain the final independent receipt at `work/production-deploy-hr5wl5el/independent-readback-result.json`. Overall product/P3 remains active.
