# Accepted screenshot-commit reconciliation (052)

Implements the original delivery plan's durable recovery and uncertain-external-result requirement. The concrete gap is a stopped App Store screenshot revision whose final image commit was accepted remotely, but whose response or local completion was lost. Existing ordinary publication retries must remain fenced.

## Contract

- Reuse the local administrator-only `-reconcile-apple` entry, exact original Request, current pending journalDigest, stopped operation key and bounded reason. Do not add Worker/MCP recovery authority or expose secrets.
- Admit only screenshot revisions at `image_commit_intent` or `material_images_processing`, with the original app/upload/file/version/build/submission/item, immutable package identity, independently approved material binding and complete persisted image IDs. All intended image commits must be positively observable as accepted/processing or ready. Missing IDs, AWAITING_UPLOAD, failure, drift and other intermediate stages stay pending.
- Revalidate the exact local IPA size/SHA256 and its remote parent-file MD5/association, original processed build, administrator-owned material manifest and PNG/JPEG inputs. The material snapshot must equal the approved manifest after removing only server-assigned IDs.
- Use only authenticated GETs to prove the original metadata-rejected review/build/item, current approved text, unchanged old display placements and every desired image's exact app-library identity, size and processing/specification. No image discovery by guessed ID, upload, text patch, resolve, placement mutation or submit occurs during recovery.
- Atomically finish the local operation as `processing` at `material_images_processing`, append provenance, and retain its app/version/successor reservations. A later read-only status observation may signal readiness; an ordinary protected continuation, with current approval and quality, performs any replacement/resubmission.
- The final SQLite CAS binds the original pending journal, source-parent journal and all current reservations. Concurrent reconciliations have one winner; drift, SQL failure, lost ownership or an unstopped/changed operation cannot produce partial completion. Exact replay is read-only and does not regress a later stage.
- Other unknown stages, partial image sets, placement changes and source drift remain explicitly blocked. This is positive-proof recovery, not automatic draft repair or a claim that every external uncertainty is solved.

## Evidence boundary

Use real SQLite, TLS, ES256 authentication and byte uploads against a local protocol provider. Synthetic IPA signing and Apple identities remain synthetic. Native Apple builds, actual accounts, enterprise signing, devices, store review/publication and overall P3 remain open. No paid model or real store/production operation; preserve all historical state and Git index.
