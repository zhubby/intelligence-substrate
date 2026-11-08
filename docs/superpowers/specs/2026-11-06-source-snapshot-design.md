# Complete governed agent source commits

The approved platform must preserve the application an agent actually produces. Text-only explicit writes currently lose tool-generated source, binary assets, deletions and executable modes. This increment replaces the worker's completion transport with a complete bounded source snapshot while retaining the existing text editor API.

## Contract

A raw tar endpoint `POST /api/v1/repositories/{id}/snapshot` consumes a complete source tree, with base commit, expected branch head, message, idempotency key and optional worker run/lease in a bounded JSON `X-IS-Source` header. The header is encoded as base64 JSON so credentials do not enter URL query strings. A human developer may submit; a worker additionally needs a live approved code-task lease for the exact repository, branch and pinned base. Main remains protected.

Archives are limited to 64 MiB and 20,000 entries, matching the established source execution boundary. Only regular files/directories are accepted. Reject escapes, links, duplicate or case/normalization-ambiguous paths, Git metadata and nonportable reserved components. Git records raw blobs and 100644/100755 modes without checking out or executing repository attributes, hooks or filters. An absent base file represents deletion; the new tree's parent is the exact approved base. Existing branches require base=expectedHead to prevent losing intervening changes.

## Durable authority

Before updating a branch, persist a source-commit operation and producer change record, bound to actor/project/repository/base/branch/snapshot digest and request key. Retries with a different binding are rejected. Ref update is compare-and-swap; the final database transaction rechecks authorization before the Git side effect. If final acknowledgement fails after ref movement, the already durable prepared record preserves provenance and an exact replay reconciles the current ref with the recorded candidate rather than constructing another commit. A different unexpected ref remains a conflict; no force push or automatic rollback occurs.

The endpoint never approves requirements, reviews, tests or releases. A source commit's own record is not quality evidence. The worker cannot use this path for analysis/quality/publish tasks or after cancellation/revision invalidation.

## Actual workspace

The trusted worker stores its source index outside the untrusted job directory. Originally tracked files remain eligible even if later ignored; additional files follow repository Git ignore rules. It scans current bytes and modes at finish, automatically includes tool-produced source/binaries and deletions, rejects selected links/special files, and streams a deterministic tar from a private sibling file. Git metadata, the downloaded validation artifact and local controller state never become source. Tool processes do not receive controller/model tokens.

The original archive's executable bits are retained as trusted metadata for Windows, where file permission bits cannot express Git executable mode. New/explicit mode changes can be requested with the write action's executable boolean. A delete action removes a single regular file. Analysis/design remain read-only at completion and cannot submit source.

## Verification boundary

Use real Git/SQLite/HTTP and actual tool-generated files; substitute only the model response when testing the worker loop. Test binary bytes larger than the text API, deletion, modes, ignore behavior, path attacks, stale head, duplicate/different replay, prepared-operation recovery, lease/project/task restrictions and independent quality. Run the full Go race suite and isolated PostgreSQL. Live model/AX cluster execution and formally published applications remain outstanding without their external environments.
