# Xiaomi Publisher Design

Implements the original V1 Android Xiaomi target using the official public automatic publication API. Continue the approved implementation in the main thread; no subagents, commits, paid model calls or real store mutations.

## Authoritative protocol

Official [应用自动发布接口操作指南](https://dev.mi.com/distribute/doc/details?pId=1134), retrieved from `https://dev.mi.com/uiueapi/doc/getArticle?aId=1134`; latest returned `updateTime` is 1705556832880. The old documentation index explicitly redirects to this documentation center. Cached raw article, tree and JavaScript discovery evidence reside in `work/xiaomi-publisher-12h23fte/official/`.

All API calls are POST to fixed HTTPS `https://api.developer.xiaomi.com/devupload`. Multipart `RequestData` and each attached file receive a lowercase MD5 in a JSON SIG envelope; encrypt the UTF-8 envelope using the administrator's Xiaomi RSA public certificate and PKCS#1 v1.5 padding, then lowercase hexadecimal. These upstream MD5 values do not replace the approved artifact SHA256. Credentials are an administrator-owned private file; no account-password fallback or account reset.

`/dev/query` reports this account's latest pushed package and create/update permissions. `/dev/push` supports new applications (`synchroType=0`) and package updates (`1`), with required icon and three to five screenshots for new applications. The official table labels appInfo a JSON string while its complete curl example uses an object; follow that explicit object example and record the compatibility boundary. Single approved release APK only; dual APK and content-only updates remain separate extensions.

The FAQ explicitly says no audit/on-shelf state API and no sandbox exist. A push result 0 confirms acceptance only. Status observations never infer reviewed, released or published from query fields or update flags. Actual store state and device acceptance use the platform's existing authorized human reconciliation/evidence flow. No production API calls are used as tests.

## Fixed destination and material binding

New `xiaomi-market` driver uses target `android-xiaomi`. AppID and BundleID are the same fixed Android package, with a fixed release signer and absolute SDK/credential paths. New optional Profile fields `metadataFile` and `metadataDigest` bind exact administrator-reviewed metadata bytes and attachment SHA256/size descriptors; omitted fields preserve existing request hashes. Other drivers reject these selectors.

Private metadata supports app name, category, search keywords, description, update description, brief, HTTPS privacy URL and optional publisher/test account information. Package/version come from the approved verified APK. No arbitrary payload, destination, delayed publication timestamp or credentials from repository code. New apps require category/keywords/description/brief and three to five screenshots; updates require update description and icon. Each attachment is a regular decoded PNG/JPEG, <=8MiB with bounded dimensions; the metadata describes exact bytes. All inputs are staged and verified before reserving a publication.

## Durable mutation and truthful lifecycle

Use an operation journal and SQLite package reservation, independent of worker leases. Before push persist intent, reread account/package edit permissions while holding the reservation, then send exactly staged bytes. Concurrent publishers cannot overwrite the same active/uncertain package. Lost response, invalid success JSON, HTTP/API error or interrupted local completion retain pending and reservation; ordinary retry never replays a write. No unknown-operation unlocking is added.

After confirmed push, persist submitted receipt, awaiting_review result and reservation release in one transaction. A later version still needs a larger versionCode and a fresh affirmative upstream update permission. Local tracking IDs include package/version; they are not upstream submission IDs. Duplicate original requests return the stored result without remote access; changed approval, material or destination is rejected. Status is observation only and returns unchanged with an explicit manual-verification reason.

## Verification and preservation

TDD uses real local TLS, RSA encryption/decryption and SQLite process reopen/concurrency. SDK signature success may be replaced only within package tests; the real CLI must reject the existing Android diagnostic APK and changed artifacts. Test credentials and screenshots are synthetic. Final vet/race includes isolated PostgreSQL and all existing release drivers; cross compilation remains distinct from target execution.

Preserve all 117 current outputs by SHA256, preview 18080/PID 36063, existing credentials/SDK/databases, staging and zero commits. New deliverables use a new prefix. Apple SDK/simulator license still returns 69 despite xcodebuild -version succeeding; no license accepted by the agent. Native Apple, real release signing, Xiaomi account/acceptance, human portal results, real devices and full P3 remain unproven.
