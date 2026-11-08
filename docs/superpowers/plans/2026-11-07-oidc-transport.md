# OIDC transport implementation

Use the existing approved platform plan and main-thread sequential workflow. Retain the feature checkout without staging, commits or publishing.

1. Capture the 049 baseline and reproduce unsafe discovered URLs and discovery/token/JWKS redirects with actual TLS/HTTP servers and authenticated callbacks.
2. Validate production issuer and required metadata endpoints. Capture a dedicated bounded client with configured trust and pre-transport URL checks; use it for discovery, exchange and keys, retaining request cancellation and existing identity authority.
3. Verify plaintext receives zero requests after the fix, no session or binding/audit is created, consumed state cannot be replayed, and valid HTTPS redirects/local development/certificate failures/configured trust remain correct. Prove stalled network requests terminate.
4. Run full Go race with isolated PostgreSQL, vet/build/tidy/diff and Linux/Windows cross-builds. Exercise actual platform processes and HTTPS SSO/browser paths; preserve failed attempts and keep the original preview untouched.
5. Document the operator boundary, export four deliverables, independently read source bytes/sizes/modes and public evidence, and verify original state before closing only049. Actual enterprise IdP and full P3 remain open.

Validation: final Go race803/2832, 43 PostgreSQL,8 regression browser flows and6 real HTTPS SSO checkpoints/four viewed images, actual process restart,8 TLS redirects,6 real negative processes/SQL readers, three standalone backup readbacks and three reproduced binaries pass. Initial858-source/859-entry output readback passed; final export/readback includes completion bookkeeping; full product/P3 remains open.
