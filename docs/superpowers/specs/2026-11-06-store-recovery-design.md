# Domestic store uncertain-operation recovery

The approved platform design requires durable publication intents, explicit reconciliation of unknown outcomes, and independent delivery evidence. This increment adds local administrator recovery to the existing Huawei, Xiaomi, OPPO and vivo connectors. It does not execute real store operations or complete P3.

## Authority and binding

`is-publisher -reconcile-store` accepts a separate administrator request, never a Worker action. It names the exact original request, journal digest, stopped operation key, operator and reason. The fixed profile, request hash, package, environment, version and original APK are checked before observation. Paths may change only if the approved bytes remain identical. Operators must stop the old publisher and exclude other store writers before reconciliation.

Recovery uses an exact snapshot compare-and-swap. It updates the operation, durable recovery history, private evidence and destination reservation in one SQLite transaction. A changed snapshot, foreign destination/version owner, missing proof or SQL failure leaves the intent blocked. Idempotent replay returns only the identical resulting snapshot, with no SDK, credentials or network required. Later continuation invalidates old recovery replay while retaining history.

## Observation paths

1. Huawei API recovery: recover an interrupted file registration or processing checkpoint only from a unique exact package (filename, SHA256, size, name and versionCode). Recover a stopped pre-submit check to processing only while the exact editable business version remains current. Recover submit intent/submitted only with the persisted business version and positively observed review/release status. Reading must not release a lock before the recovery transaction. Processing/review retains Huawei's destination lock; terminal observations release only its own lock.
2. OPPO API recovery: only submit-intent/submitted; require exact ordinary application, version name/code/type/ID, APK MD5 and size even when the asynchronous task is pending or failed. Query POST and authentication exchanges are semantic reads; no upload/update call is allowed.
3. Xiaomi/vivo portal recovery: only an existing final push/submit intent (or submitted checkpoint). Require a private, digest-verified JSON report, 1–8 digest-verified attachments, exact operation/journal/request/profile/APK/material binding, local operator identity, portal submission reference and a recent observation. Store the report and attachment bytes transactionally. This is an operator assertion of acceptance, explicitly labeled `PORTAL_ACCEPTED_UNVERIFIED`, with state `processing`; it proves neither review nor publication. vivo's sandbox remains a distinct environment and cannot establish production delivery. No channel request is made on this path.

API recovery may report processing, awaiting review/release, rejection, halt or publication according to exact upstream observations. Published state still requires independent device verification in the control plane. Terminal or other recovered states are imported with the existing human `releases.reconcile` report gate; a Worker is not granted recovery authority.

## Retention and replay prevention

History is stored separately from connector receipts, so later checkpoints cannot overwrite it. Inspection includes all recovery records, observed results and evidence digests; private evidence bodies remain in the publisher database and its backup. A permanent recovery-version reservation also prevents Huawei/Xiaomi from reusing a recovered version under another operation key, even if a channel later hides it. Existing OPPO/vivo permanent ownership must still match. Recovery never abandons an operation or authorizes reusing uncertain versions.

Earlier upload stages, absent/ambiguous responses and insufficient artifact proof remain blocked. There is no generic unlock, retry of an upstream write, portal-as-API proof, or inferred publication from a successful task, latest pushed package or general sale status.

## Validation and workspace boundaries

Real SQLite and local HTTPS connector fixtures cover accepted-but-lost responses, exact bytes and bindings, stale input, proof provenance, private evidence retention, retry/reopen, concurrent recovery, mutation during observation, own-lock/version ownership and transactional rollback. CLI modes remain exclusive and strict JSON rejects unknown fields/trailing objects. Existing connector tests, Go vet/race with a dedicated PostgreSQL cluster, and 21 cross-platform command builds provide regression evidence. This increment changes no UI, native blueprint or model integration; their prior evidence is not rerun or relabeled.

Preserve all 141 existing outputs, 261 staged entries, zero commits, the existing preview PID 36063 and all `web/dist` bytes. Use task-owned `work/store-recovery-*` for generated state. Apple SDK acceptance remains a human prerequisite. Overall P3 remains open until two real applications complete all declared platforms/channels and independent acceptance.
