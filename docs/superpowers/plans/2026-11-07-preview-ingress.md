# Development preview production ingress verification

Continue the approved platform implementation by validating the existing static preview ingress. Work sequentially in the current feature checkout and preserve the staged index. No formal release or external publication is authorized by this increment.

## Tasks

- [x] Capture source/output/index/preview/Docker/AVD baseline and recheck Apple license.
- [x] Verify official Caddy download against release metadata and published checksums.
- [x] Reproduce actual reverse-proxy failure and inspect private runtime logs for short-lived URL leakage before changing deployment files.
- [x] Apply the smallest proven logging/configuration correction and document the deployment boundary.
- [x] Add a repeatable local production-mode HTTPS verification using private certificates and two sibling hostnames. Do not alter the system trust store or broadly ignore TLS failures.
- [x] Verify real assets/interaction, secure host-only session isolation, exact binding headers, denied control-plane routes, stop/logout revocation, proxy-failure logging and recovery.
- [x] Run relevant existing preview regression and configuration/format checks. Broaden only if source changes warrant it.
- [x] Review the complete change sequentially; verify preservation and export sanitized report/runbook/evidence/source snapshot; independently read back final outputs.

## Completion boundary

Local HTTPS through the tested Caddy version and Chrome proves this deployment path only. Enterprise DNS/certificates/CDN/APM, other proxies and browsers, capacity, actual production deployment, native devices/signing/stores and the original two-application P3 acceptance remain open.
