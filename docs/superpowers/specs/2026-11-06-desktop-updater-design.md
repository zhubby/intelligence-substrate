# Governed desktop updater

This implements the approved product requirement for publicly distributed, signed Windows/macOS applications with updates. It extends the existing Ed25519 feed and immutable distribution service, without changing release approval or independent quality gates.

## Contract

An administrator installs `is-updater` with a fixed JSON profile: state directory, HTTPS distribution origin, application, target, channel, pinned Ed25519 public key, expected native signer and OS package identifier. Profiles are bound to durable state; trust changes cannot silently reuse or reset the sequence floor. No private signing keys or control-plane credentials are required.

Windows additionally pins the exact MSIX Publisher for both package metadata and installed receipts, so another publisher's same-name application cannot confirm installation. An optional administrator-owned CA PEM supports enterprise HTTPS without disabling TLS verification. Unix state directories require private 0700 access; Windows privacy depends on administrator-managed ACLs.

The client offers JSON `check`, `stage`, `install`, `status` and `reconcile` actions. `check` verifies signed provenance and records the highest sequence/version. `stage` downloads only the signed package's exact same-origin immutable URL, limits bytes, verifies SHA256/size, native trust and package identity/version. `install` requires the caller to approve the staged SHA256 explicitly and revalidates the bytes, signature, index expiry and installed version before reserving an installation. Native installation is never triggered by checking or downloading.

First installer formats are notarized, signed, single-component macOS PKG and signed Windows MSIX. The PKG receipt identifier/version and MSIX identity/version must match the fixed profile and feed before installation. Numeric major.minor.patch maps to MSIX major.minor.patch.0. EXE, MSI, product/distribution PKG and other MSIX revisions remain unsupported. Applications can standardize these supported formats without changing the required Windows/macOS product targets.

## State and recovery

SQLite serializes checks, staging and installation reservation across processes. The client persists the exact signed index, sequence, immutable release identity, package path, observed installed version and installation attempt. Same-sequence conflicting payloads, lower sequences, lower versions and same-version different packages are rejected. Renewals may increase the sequence while preserving release identity.

An installation is durably reserved before invoking the OS. A second process or restart cannot execute the installer again. Failure, interruption or uncertain completion leaves `installing` or `reconciliation_required`. A receipt alone cannot establish that an installer/service has finished. Before `reconcile`, the operator must verify termination using native diagnostics and explicitly confirm the exact persisted attempt ID. Reconciliation then only reads the native installed receipt; it never invokes an installer. The exact candidate version observed in the OS receipt may resolve the confirmed-stopped attempt as `installed`. Other results remain explicit for operator investigation. Successful OS receipt observation does not establish application startup, data compatibility or independent business acceptance.

The client uses native OS privilege boundaries; it does not invoke sudo, accept agreements or change elevation policy. macOS unattended installation requires the enterprise's existing authorized management context. Windows MSIX operates in the invoking user's package context. Staging directories and their database must be private to that context, with OS ACLs configured by the administrator; the updater is not setuid and does not defend against an administrator modifying its own trust store/state.

## Evidence boundary

Tests use actual TLS, Ed25519, SHA256, SQLite, files and ZIP/XML package metadata. Native installer success is substituted only inside test dependencies, with assertions on durable state and at-most-once dispatch. Local unsigned package rejection runs the real macOS tools. Cross compilation is recorded separately from actual Windows installation. There are no real signed installer credentials, Windows node or production package-installation claims in this increment.

The overall P0-P4 plan remains active: Flutter desktop packaging and target-system integration acceptance, real OS installation/upgrade, physical/device business acceptance, AX/models, store channels and enterprise operations still require further implementation or live verification. The subsequent Flutter integration plan implements application-facing source and process bridging without claiming native package acceptance.
