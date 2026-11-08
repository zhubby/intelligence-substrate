# SSO identity boundary implementation

Execute sequentially on the existing feature checkout; no staging, commits or publishing.

1. Record the 048 baseline and reproduce cross-account subject binding and invalid state acceptance with actual authenticated callback tests.
2. Enforce unique immutable subject binding under current SQL authority, reject invalid subjects and consume valid unexpired OIDC states once in a request-bound transaction.
3. Check concurrency and rollback on SQLite and two independent PostgreSQL connections; retain legacy binding, account-disable and OIDC compatibility tests. Verify portable backup/restore keeps identity bindings but excludes login/session credentials.
4. Run full Go race/vet/build/tidy, affected browser checks and an isolated real HTTPS OIDC/platform process/restart flow. Preserve all failed evidence. Native/device/store/production acceptance remains separate.
5. Document the operator recovery boundary, export source/report/runbook/evidence, independently read back all bytes and original state, and close only 048 after final verification. Full product/P3 remains open.

Validation: final Go race795/2799, 43 PostgreSQL,8 existing browser flows and6 real HTTPS SSO checkpoints/four viewed images, actual process restart, two-instance PG callback race, four standalone backup readbacks and four reproduced binaries pass. Initial852-source/853-entry output readback passed; final export/readback includes completion bookkeeping. Full product/P3 remains open.
