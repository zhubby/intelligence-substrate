# Trusted Android Signing Implementation Plan

> For agentic workers: execute sequentially using superpowers:executing-plans. User instructions prohibit subagent dispatch.

**Goal:** Connect unsigned Android release builds to administrator-owned signing and independent verification without exposing private keys to repository tools.

**Architecture:** Existing build tasks carry an explicit unsigned artifact stage or a protected signing transformation. A fixed Python signing program validates and transforms exact APK bytes with an administrator keystore. Control-plane leases derive immutable input/output provenance; the existing quality scheduler verifies only final packages.

**Tech Stack:** Go control plane/Worker, Python standard library/SQLite, Android SDK36, Flutter, Java.

**Spec:** ../specs/2026-11-06-trusted-android-signing-design.md

## Global Constraints

- Preserve261 staged records, zero commits,201 previous outputs and the original18080 preview/static files; no Docker mutation.
- No production/store/model/notification actions or Apple agreement acceptance. Test keys are generated only within the new evidence root.
- Private signing configuration stays outside repository workspaces. No private values in task/lease, command line, environment or reports.
- Actual signed output receives fresh independent evidence; no unsigned, simulator or synthetic result satisfies physical/enterprise/P3 acceptance.
- All shell commands and local verification wrappers use RTK. Product SDK/Flutter subprocesses remain portable and do not require RTK in enterprise CI. Main thread only.

### Task1: Unsigned reference build

**Files:** scripts/new-app.py; scripts/build-reference-client.py; scripts/test_new_app.py; scripts/test_build_reference_client.py.

**Interface:** `--android-unsigned-release` records an explicit scaffold choice; `--target android-unsigned-apk --aapt ABS --apksigner ABS` consumes it, runs release checks, and emits signing=unsigned-apk with accurate SDK inspection metadata.

- [x] Add generation/build regressions first: flag preconditions, unfamiliar Gradle text rejection, debug signing retained, HTTPS and missing-tool refusal, failed signature/metadata checks, exact unsigned file and no overwrite.
- [x] Run `rtk proxy python3 -m unittest discover -s scripts -p 'test_*app.py' -v` and the focused build tests; retain expected failures.
- [x] Implement exact one-template signingConfig replacement plus independent real SDK output checks. Do not edit source at build time or silently reinterpret debug artifacts.
- [x] Generate a fresh app, preserve exact Git source, run Flutter analyze/26 widget checks/release compilation and verify actual APK metadata/signature state.

Generation regression example:

```python
result = self.generate(destination, '--create-platforms', '--android-unsigned-release')
self.assertEqual(result.returncode, 0, result.stderr)
gradle = (destination / 'client/android/app/build.gradle.kts').read_text()
self.assertIn('signingConfig = null', gradle)
self.assertNotIn('signingConfig = signingConfigs.getByName("debug")', gradle)
```

### Task2: Private trusted APK signer

**Files:** scripts/sign-android-apk.py; scripts/test_sign_android_apk.py; deploy/signer.android.example.json; docs/android-signing.md.

**Interface:** `main()` reads one strict request from stdin, uses fixed private config, records intent in SQLite and returns a signed CAS object plus public binding. `--inspect OPERATION_KEY` only reads durable state. `execute(config, request)` returns a result with artifactPath,artifactDigest,artifactSize,certificateSHA256,packageName,versionName,versionCode,payloadDigest,requestBinding.

- [x] Add adversarial input/profile/archive and durable replay/concurrency tests before implementation. Test fixtures explicitly do not certify Android signatures.
- [x] Implement bounded APK inspection and exact SDK metadata/signature checks, secure private paths, intent-before-signing, atomic CAS/receipt persistence, replay and uncertain-state refusal.
- [x] Execute real keytool/zipalign/apksigner using a new ephemeral certificate and actual Task1 APK; independently verify exact certificate, payload, alignments, negative inputs, replay and interrupted intent. Keep password bytes out of captures.

CLI contract example:

```json
{"protocolVersion":"1","action":"sign","operationKey":"sign-example","projectId":"project_example","goalId":"goal_example","runId":"run_example","repositoryId":"repository_example","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","target":"android-huawei","artifactId":"artifact_example","artifactDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","artifactSize":12345,"artifactPath":"/private/controller/input.apk","expectedSigner":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
```

### Task3: Governed platform transformation and Worker

**Files:** internal/platform/android_signing.go and tests; internal/platform/goals.go; internal/platform/tasks.go; artifact upload handler; internal/worker/android_signing.go and tests; internal/worker/runner.go; web task/worker views where capability display requires it.

**Interface:** Ordinary build artifactStage=unsigned produces unsigned-package. Signing build role=release uses signingMode=android-apk,inputArtifactFrom,expectedSigner; lease signingInput is server-derived. Worker Config.Signers maps target to a fixed absolute program.

- [x] Add rejection tests for forbidden raw signingInput/commands, cross-project/revision/source/run inputs, unapproved or incomplete dependencies, wrong roles, unsigned candidates and stale leases.
- [x] Resolve protected input when claiming, derive upload metadata from the lease and verify it again at completion. Keep unsigned-package excluded from existing quality and release logic.
- [x] Add actual HTTP/Git/Worker tests using the trusted-program contract; assert missing config cannot run repository tools, secrets/tokens are absent, wrong digest/cert/output fails, duplicate completion is safe and source/permission changes fail closed.
- [x] Bind existing automatic quality to the signed final package and run a real release-signed emulator sample. Verify physical android-device gate rejects release creation and persists zero candidates.

Task data example:

```json
{"id":"sign","title":"Sign exact release APK","kind":"build","role":"release","runtime":"linux","target":"android-huawei","dependsOn":["unsigned"],"signingMode":"android-apk","inputArtifactFrom":"unsigned","expectedSigner":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
```

### Task4: Verification and export

- [x] Run relevant Python and Go race/vet checks, full Go/PostgreSQL or browser checks when touched runtime/transactions/UI require them; retain initial failures and explicit environment limits.
- [x] Review new trust boundaries sequentially in the main thread and inspect actual native screenshots. Do not claim an independent code reviewer.
- [x] Stop only owned processes; verify original resource/index/output hashes. Export source/report/runbook/evidence and actual report bytes, independently read source/Git/SQL/output bindings, close only this increment after all criteria pass.
- [x] Keep overall P3 and enterprise signing/AAB/iOS/desktop/physical/store/production acceptance open unless independently demonstrated.

## Verified completion

Final actual run: work/trusted-android-signing-5fgku0dn/actual-platform-reviewed.438/1600 Go race including isolated PostgreSQL,81 Python,50 standard browser flows and14 final related browser flows passed. Main-thread trust-boundary and native screenshot review complete. Readback verified463 platform source files,139 exact sample Git files/modes,8 deliverables,68 identical unsigned/signed payload files,read-only SQLite bindings and201 historical outputs. Only this increment is complete; Apple license69,enterprise identity/physical/store/production and overall P3 remain open.
