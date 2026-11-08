# Governed source integration

The approved platform needs to combine independently produced changes, resolve content conflicts and independently verify the combined application. Existing strict source binding remains the default. Add an explicitly approved `sourceMode:"integrate"` to code work items; reuse developer/code workers, budget, leases, source snapshots and downstream quality gates. No direct main updates or authority to lower verification.

## Fixed inputs and preparation

Integration source binding records all selected successful producers and unique parent commits (maximum eight unique source commits, combine larger graphs through integration tasks). Its base is the declared full commit or a selected producer; a separately declared base is also an original approved parent and counts toward the eight-parent limit; the default target is a dedicated `integration/{goalId}/{logicalId}` branch. Existing targets must match the chosen base. Missing producer inputs, multiple repositories without selection, or unrelated histories block rather than choosing another branch. Ordinary code/build/security still reject unintegrated inputs.

`sources.prepare {runId,leaseToken}` requires the current integration code lease. Git merge-tree prepares the exact selected parents without checking out or executing repository code, with hooks/global configuration disabled, a whole-operation timeout and bounded archive validation. Conflicts are machine-parsed paths, including binary and deletion cases; all paths must fit portable source rules. Sequential intermediate commits remain private controller refs and never become parents of the final output. Preparation is idempotent by immutable task binding and reusable if a lease expires before source submission.

The run records integrationId, workspace commit, conflict paths and a resolution digest. The persisted integration object records task/plan/revision, binding digest, selected parents and prepared workspace. A model-free clean integration submits the prepared tree immediately. A conflicting integration supplies the worktree and fixed input versions to the approved command or Agent. Per-input reads/selections stay in controller-owned directories, not the tool tree.

## Explicit conflict decisions and final source

`sources.resolve` accepts a bounded batch of conflict-path decisions (SHA256, executable bit, or deletion), only for the current run and preparation. The Agent gains read_input/take_input/resolve actions. A successful approved integration command records decisions for every conflict path; unresolved markers or changed bytes are still rejected by the server. Decisions bind exact uploaded bytes/modes, so subsequent edits require another resolution decision. Deleted original files may be replaced by a directory with child files.

Integration snapshots add only integrationId/resolutionDigest to the bounded metadata header. They infer all parents from the protected binding, require a decision for every conflict, verify actual file bytes/modes/deletions and reject remaining Git conflict markers. They may have the same tree as a parent (a legitimate binary choice or already integrated history). Final commits have original input parents, preserving provenance without committing intermediate conflict trees into final history. Durable prepared source/CAS/replay semantics remain intact. Text commit cannot bypass integration checks; completion requires the current-run confirmed integration snapshot and ancestry of every selected parent.

## Downstream and repair

Downstream tasks select the integration task's actual successful result and build/test the exact SHA even if its target branch moves later. Independent quality is still required; Git history and conflict decisions do not establish correctness. Repair tasks start at the failed integrated artifact using a fresh repair branch and ordinary code mode, then reuse protected build and test recipes. Pending integration work cannot be split into duplicate integration work.

The UI exposes integration inputs, preparation and outstanding/resolved conflict paths in existing details. API/model/docs describe the approved mode and decisions. Integration does not publish, approve plans, or merge protected main. Cross-lease confirmation after a source branch update remains a separate outstanding recovery requirement.

## Acceptance

Real Git/SQL/API tests cover clean/content/binary/delete conflicts, exact parents, no-op integration, reusable preparation, immutable inputs, resolution byte/mode mismatch, markers, wrong run/repository/revision, text bypass, retry/CAS and strict default behavior. A real HTTP/native execution chain uses actual parallel source changes, merge preparation, deterministic model/approved command conflict repair, build and independent QA; failure can enter existing repair/rebuild/retest. Full vet/race with isolated PostgreSQL, three-platform Go builds, workbench build/browser acceptance, main-thread review and new exports preserve previous outputs and 18080. Live model/AX and formal multi-platform delivery remain unproven.
