# Controlled Android App Bundle delivery

This increment implements the Google Play package path in the approved platform specification. Overall P3 still requires two applications on all required channels; no local check substitutes for store, production or physical-device acceptance.

## Design

Use the existing developer unsigned build → dedicated signing Worker → independent protected quality flow. Google Play tasks use target android-google-play and signingMode android-aab; domestic APK targets retain android-apk. The target fixes the format and cannot be changed by a signing request. Server-derived source, input digest, signer and operation key retain all existing lease/authority checks, repair bounds and unknown-result behavior.

The reference builder gains android-unsigned-aab, requires the explicit unsigned-release blueprint and HTTPS API URL, runs analyzer/tests and arm64 release appbundle build. It validates bounded ZIP structure, bundletool validation and protobuf manifest identity/versions/SDK/non-debug state before publishing immutable bytes and provenance.

Extend the administrator-owned signer without running repository code. An AAB profile contains private keystore/password paths, Java home, pinned bundletool path/SHA256 and fixed verifier source path. Private inputs remain outside durable state. Jarsigner signs a staged input only after a durable intent. A Java JarFile verifier reads every payload entry to verify its cryptographic signature and exact upload certificate, rejecting unsigned/tampered/multiple-signer content. The unsigned and signed payload file maps must match, excluding only JAR signing metadata; package/version metadata must remain identical. Successful replay reuses exact bytes; interrupted/failed intent never signs again automatically.

Protected quality independently validates the exact signed AAB and emits an uploadable report containing its byte digest, upload certificate, source tree, bundle metadata and payload hashes. This is CI bundle validation, not android-device evidence. The native acceptance additionally uses pinned bundletool to derive a universal APK with a separate temporary installation certificate and records AAB → generated APK → actual installed byte digests. Those certificates and bytes cannot be called Google Play's app-signing identity or distributed artifacts. Store publication remains unattempted, and missing physical-device evidence must still block release creation.

## Acceptance

- Explicit AAB/target pairing, server-derived inputs, dedicated Worker isolation, final quality and two-round repair work for Google Play; APK behavior remains covered.
- Reject mixed mode/target, forged context, wrong bytes/certificate, existing/partial JAR signatures, unsafe ZIPs, unsigned entries and changed payload. Signing uncertainty preserves its original intent.
- Actual Flutter builds and Java/bundletool signatures are exercised through real HTTP/Git Workers, with isolated database and no model/store/production calls.
- Unit/race/PostgreSQL restore/concurrency and browser checks cover changed behavior; exports are read back against source, SQLite and exact artifacts.
- Preserve471 original source files,219 prior outputs,261 index records,zero commits,original18080/PID36063/3 static files,Docker37/23/15 and original AVD21 files. Use only this increment's private directory; do not accept Apple agreements, change global tool selections or use subagents.
