# Enterprise OIDC transport boundary

This increment implements the approved enterprise SSO and controlled-credential requirements. Execution is sequential on the existing feature checkout; preserve staging, old outputs, the original preview, Docker, AVD and keychain state. No real enterprise credentials or external publication are required.

Production SSO must validate the issuer and the discovered authorization, token and JWKS URLs as absolute HTTPS URLs without userinfo or fragments. Fail configuration before enabling login when a required endpoint is missing or unsafe. Identity, signature, state, nonce, PKCE, preprovisioning and current-membership checks remain in force.

Discovery, token exchange and JWKS fetch must use a dedicated client captured at configuration. Preserve its trust roots, proxy, redirect policy and shorter timeout, enforce a bounded request lifetime, and reject every unsafe destination before transport, including redirects. Request context values must not replace the configured client. Support legitimate HTTPS redirects and endpoints on different HTTPS origins; do not invent a same-origin requirement. Local development mode may continue using HTTP.

The platform validates the initial browser authorization endpoint. Navigation and redirects performed by the identity provider after the browser leaves the platform are the provider/operator's responsibility; this client policy cannot govern them. Likewise HTTPS checks are not a destination allowlist, enterprise IdP integration, MFA, SCIM, offboarding or production network policy.

Prove behavior against real isolated TLS/HTTP servers with signed ID tokens, actual SQL and nonce/PKCE. Unsafe redirect tests must observe zero requests at the plaintext receiver, no session and no identity/audit mutation. Preserve the original failing evidence. Validate positive TLS redirects, configured trust across callbacks, development compatibility, certificate failure and bounded stalled requests. Run the full database/race/build and affected browser checks before source/output readback. Complete only this increment; native/device/signing/channel/production and full P3 acceptance remain separate.
