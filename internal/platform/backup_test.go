package platform

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestBackupRestorePreservesArtifactsGitAndAudit(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "backup-app"})
	repo = must(t, s, a, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Add file", "files": M{"app.txt": "backed up"}})
	_, e := s.Upload(context.Background(), a, M{"goalId": g.ID, "repositoryId": repo.ID, "commit": S(repo.Data, "lastCommit"), "target": "web", "name": "package.txt"}, bytes.NewBufferString("backed up"))
	if e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if e = s.Backup(context.Background(), archive); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(t.TempDir(), "restored")
	if e = Restore(context.Background(), archive, root, ""); e != nil {
		t.Fatal(e)
	}
	if e = Restore(context.Background(), archive, root, ""); e == nil {
		t.Fatal("restore overwrote an existing directory")
	}
	restored, e := Open(filepath.Join(root, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	svc := &Service{Store: restored, Root: root}
	if e = svc.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = restored.Get(g.ID); e != nil {
		t.Fatal(e)
	}
	audits, _ := restored.Audits(p.ID)
	if len(audits) < 4 {
		t.Fatal("audit history was lost")
	}
}

func TestInvalidBackupRollsBackExternalDatabase(t *testing.T) {
	s, _, _ := fixture(t)
	if _, e := s.Store.DB.Exec(`UPDATE audit SET body='{}'`); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(t.TempDir(), "invalid.tar.gz")
	if e := s.Backup(context.Background(), archive); e != nil {
		t.Fatal(e)
	}
	external := filepath.Join(t.TempDir(), "external.db")
	if e := Restore(context.Background(), archive, filepath.Join(t.TempDir(), "restored"), external); e == nil {
		t.Fatal("invalid audit chain was restored")
	}
	store, e := Open(external)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	for _, table := range []string{"resources", "audit", "identities"} {
		var count int
		if e = store.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); e != nil {
			t.Fatal(e)
		}
		if count != 0 {
			t.Fatalf("failed restore left %d rows in %s", count, table)
		}
	}
}
