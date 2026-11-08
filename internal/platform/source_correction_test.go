package platform

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// The code is a protocol guarantee: only a pre-candidate scope rejection can
// authorize editing the failed source before another attempt in the same run.
func TestSourceCorrectionOnlyUnpreparedSnapshotScopeHasCorrectionCode(t *testing.T) {
	f := newWriteScopeFixture(t, []any{"backend/"})
	run := f.claim(t)
	files := writeScopeFiles()
	files["frontend/ui.txt"] = []byte("not approved")
	_, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, writeScopeInput(run), bytes.NewReader(sourceTar(t, files, "")))
	var fault *Fault
	if !errors.As(err, &fault) || fault.Status != 409 || fault.Code != "source_write_scope_rejected" {
		t.Fatal("pre-candidate scope rejection is not machine-readable", err)
	}
	_, err = f.s.Command(context.Background(), f.worker, "repositories.commit", M{"id": f.repo.ID, "runId": run.ID, "leaseToken": run.Data["leaseToken"], "branch": Obj(Obj(run.Data["task"])["sourceBinding"])["targetBranch"], "baseCommit": Obj(Obj(run.Data["task"])["sourceBinding"])["commit"], "message": "Legacy still stops", "files": M{"frontend/ui.txt": "not approved"}})
	if !errors.As(err, &fault) || fault.Code != "conflict" {
		t.Fatal("legacy entry granted an unprepared-snapshot correction receipt", err)
	}
	ops, _ := f.s.Store.List(f.p.ID, "source_commit", f.g.ID)
	if len(ops) != 0 {
		t.Fatal("correction receipt followed an adopted candidate", ops)
	}
}

func TestSourceCorrectionPreparedCandidateDriftNeverGrantsEditingReceipt(t *testing.T) {
	f := newWriteScopeFixture(t, []any{"backend/"})
	run := f.claim(t)
	files := writeScopeFiles()
	files["backend/main.go"] = []byte("package backend\n")
	body := sourceTar(t, files, "")
	if _, err := f.s.Store.DB.Exec(`CREATE TRIGGER fail_correction_finalize BEFORE UPDATE ON resources WHEN NEW.kind='source_commit' AND NEW.status='committed' BEGIN SELECT RAISE(ABORT,'injected finalize fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, writeScopeInput(run), bytes.NewReader(body)); err == nil {
		t.Fatal("prepared candidate fault was not exercised")
	}
	if _, err := f.s.Store.DB.Exec(`DROP TRIGGER fail_correction_finalize`); err != nil {
		t.Fatal(err)
	}
	ops, _ := f.s.Store.List(f.p.ID, "source_commit", f.g.ID)
	if len(ops) != 1 || ops[0].Status != "prepared" {
		t.Fatal("missing real interrupted candidate", ops)
	}
	task := f.task(t, "edit")
	task.Data["writePaths"] = []any{"frontend/"}
	f.put(t, task)
	_, err := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, writeScopeInput(run), bytes.NewReader(body))
	var fault *Fault
	if !errors.As(err, &fault) || fault.Code != "conflict" {
		t.Fatal("prepared approval drift was treated as an unadmitted editable snapshot", err)
	}
	current, _ := f.s.Store.Get(ops[0].ID)
	if current.Status != "prepared" || S(current.Data, "commit") != S(ops[0].Data, "commit") {
		t.Fatal("prepared candidate was replaced after drift", current)
	}
}
