# App Store reviewed materials

This increment implements the approved platform's immutable release materials and trusted App Store metadata management. It extends the existing upload/processing/continue/review flow. It does not require an additional production approval to implement or exercise local tests.

## Approval and scope

A release can reference one `report` artifact for `ios-appstore` using `storeMetadataArtifactIds`. The artifact must belong to the same goal, requirement revision, repository and commit as its package. Its SHA256 is included in the release binding and propagated through publish, status and continuation tasks. The approver can download the actual JSON. Its producer cannot approve that candidate.

The publisher's optional, administrator-owned `metadataFile` and `metadataDigest` select that exact private JSON. Managed publication requires an identical request `metadataDigest`; omission cannot silently select portal materials. Profiles and old journals with no materials remain compatible and are explicitly shown as unmanaged. Changes require a new candidate and approval, not modification of an accepted operation.

The manifest specifies schema version 1, app ID, bundle ID, marketing version and one to eight existing locales. Each locale contains the complete version text (description, keywords, support/marketing URL, promotional text and what's new), complete app-info text (name, subtitle, privacy policy/choices URLs and privacy policy text), and one to four screenshot groups. Groups name a runtime-catalog placement group and one to ten ordered PNG/JPEG files with absolute paths, byte sizes and SHA256. Total assets are bounded; all bytes are privately staged and fully decoded before any remote mutation. No credentials or demo accounts belong in this manifest.

## Current Apple API

Official documentation retrieved on 2026-11-06 deprecates `appScreenshots` and screenshot sets. Use **App Asset Library** only: app-owned library, runtime reference catalog, immutable image reserve/PUT/commit, placements and ordering requests. Image commit sends only `uploaded: true`; it does not invent the deprecated MD5 field. Runtime catalog validates IOS device platform, feature, category, group count, accepted spec, exact dimensions, MIME, extension, alpha and file-size limits. No baked-in device-family table.

The enterprise prepares a unique editable app-info resource and the existing version/localizations. Managed locales must exactly match both complete parent collections; extra/duplicate/paginated locales are blocked. The prepared version must initially have no placements or legacy screenshot/preview sets. This increment does not delete or migrate existing media, create locales, manage age/export/privacy declarations, credentials, contracts, videos, custom pages or rejected-version resubmission.

## Execution and evidence

1. Validate/stage the manifest and every image, verify the signed IPA and target app/version; discover exact localization IDs, empty media, library and runtime catalog using GET only.
2. Atomically reserve the operation, app destination and app/version/build identity. Save the material digest, expected text and ordered image identities without private local paths. Every subsequent write has a durable intent and verifies the pending operation binding and reservation.
3. Patch version/app-info localizations; reserve, upload and commit images; then perform the existing IPA upload. Each known ID and successful phase is persisted. An ambiguous response or failed journal write remains pending; new keys, reopen and concurrency cannot replay it.
4. Return `processing` and release the worker lease. GET status verifies exact immutable image identities, processing/spec/dimensions and the existing IPA. It never creates placements. Only when all are ready is a continuation requested.
5. Continuation rechecks approved material text and absence of unexpected media, creates placements and orders each group, then re-reads complete ordered placement collections and exact image identities before linking the build. Recheck material text and media again immediately before final review submit. Drift blocks submission and retains the journal for operator reconciliation. Portal activity must be exclusive while a managed operation runs; Apple has no transaction/conditional review submit covering all these resources.
6. Approved publication still needs independent real-device/channel evidence to become delivered. App lock is retained while processing and through uncertain stages; confirmed review submission releases it atomically with completion. Version/build ownership is permanent. Metadata intermediate-stage auto-recovery is unsupported. Existing upload/review read-only recovery must verify the managed snapshot before a CAS and release a submitted app lock only in that transaction.

Uploaded SHA256 is established by local staging and exact authenticated upload-range transfer; Apple's new image API provides no source SHA256/checksum. Persist the returned immutable image ID, expected bytes/name and processed spec/dimensions. Do not claim an independently downloaded original-file hash or a live Apple acceptance test.

## Validation and preservation

Use test-first Go/SQLite/local TLS tests with independent request/byte assertions, persisted reopen/replay/concurrency checks, metadata drift, async image waiting and recovery checks. Test candidate binding and worker propagation plus the actual Web form/review flow. Run vet/full race with a task-owned PostgreSQL cluster, Web build/browser tests and 21 cross-compilations. Other systems are compile-only.

Preserve the existing index, unborn branch, 129 output files and preview PID 36063/port 18080. Use a separate test service. No staging, commit/push, paid models, live account writes or agreement acceptance. Xcode's SDK probe still returns 69 and native Apple build Task 2 remains open.
