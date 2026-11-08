# Android physical-device business quality (051)

Implements the original product's independent Android device requirement and the concrete adapter gap recorded by050. It does not redefine complete product/P3 acceptance.

## Contract

- A trusted quality Worker consumes the exact `IS_ARTIFACT_PATH`, exact application source and an administrator-owned0600 JSON profile outside the repository. Pipeline method is `device`, environment is `android-device`, and `reportPath` is an immutable archive.
- The profile fixes one USB serial, ADB server port, manufacturer/model/build fingerprint/SDK, approved package/versionCode/versionName/release-certificate SHA256, business mode and dedicated HTTPS test-API origin. Booking additionally fixes one test slot. Unknown keys, HTTP/credentials/query/fragment, debug signatures, different versions, emulators, offline/unauthorized/ambiguous devices and occupied package names fail before installation.
- ADB must already be listening on the declared loopback port. The adapter never starts or authorizes devices. Device ownership locks are shared across ADB ports on this host. USB listing and Android properties are operational identity checks, not cryptographic hardware attestation.
- The adapter installs only an initially absent package, compares device `base.apk` SHA256 to the exact platform artifact, performs blueprint feedback or booking UI/API business checks and app relaunch, then checks identity and installed bytes again. It never upgrades or replaces an existing app.
- The HTTPS client verifies certificates, rejects redirects, bounds requests and does not retry writes. Tests use a dedicated empty feedback backend or exactly one unreserved declared booking slot. Actual UI writes must be observable at that same API; booking cancellation ownership is checked. Feedback test data remains in the dedicated backend; uncertain writes require operator reconciliation.
- Device network settings, trust stores, production backends and existing applications are not changed. Only a package whose initial absence and exact installed bytes were established may be removed; cleanup errors make the run fail. Unknown identity/bytes preserve the package and record the issue.
- Private reports retain the exact source map, APK/version/certificate, bound device observations, HTTPS API scope, command diagnostics, screenshots/UI trees, named checks and cleanup. Backend restart, offline recovery, upgrades, store publication, network isolation, enterprise identity and hardware attestation are explicitly unverified by this adapter.
- First blueprint scope is flutter-go-v1 feedback/booking with an arm64 release APK. Google Play delivery transforms AABs into channel-distributed APKs; device-side APK/AAB mapping remains separately required; this adapter does not mark an APK as Play-distributed.

## Evidence boundary

No physical Android device is currently attached to the default ADB server. Local deterministic device/tool processes and real TLS/API tests verify adapter behavior only. They must never count as actual physical-device, enterprise signature, store or P3 evidence. A real device run is required before the adapter itself has native acceptance evidence.
