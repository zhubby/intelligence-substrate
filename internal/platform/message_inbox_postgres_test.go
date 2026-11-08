package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresMessageInboxAtomicConcurrencyAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := NewID("inbox_test")
	if _, e = admin.DB.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	connection, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	query := connection.Query()
	query.Set("search_path", schema)
	connection.RawQuery = query.Encode()
	store, e := Open(connection.String())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	other, e := Open(connection.String())
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	svc := &Service{Store: store, Root: t.TempDir()}
	second := &Service{Store: other, Root: svc.Root}
	owner := Actor{ID: "inbox-postgres-owner", Kind: "human", Admin: true}
	project := must(t, svc, owner, "projects.create", M{"title": "PostgreSQL durable Agent context"})
	f := newMessageFixtureOn(t, svc, owner, project)
	message := f.send(t, "actual-inbox", f.worker.ID, M{"contract": "UTC survives concurrent delivery and database restore"})
	if _, e = store.DB.Exec(`CREATE FUNCTION reject_inbox_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected receipt failure'; END; $$`); e != nil {
		t.Fatal(e)
	}
	if _, e = store.DB.Exec(`CREATE TRIGGER reject_inbox BEFORE INSERT ON resources FOR EACH ROW WHEN (NEW.kind='message_receipt') EXECUTE FUNCTION reject_inbox_receipt()`); e != nil {
		t.Fatal(e)
	}
	in := M{"runId": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken")}
	if _, e = svc.Command(context.Background(), f.worker, "messages.receive", in); e == nil {
		t.Fatal("receipt SQL failure was ignored")
	}
	stored, e := other.Get(message.ID)
	if e != nil || stored.Status != "pending" {
		t.Fatal("failed receipt transaction acknowledged message", e, stored)
	}
	if _, e = store.DB.Exec(`DROP TRIGGER reject_inbox ON resources`); e != nil {
		t.Fatal(e)
	}
	type delivery struct {
		r Resource
		e error
	}
	results := make(chan delivery, 2)
	var wg sync.WaitGroup
	for _, service := range []*Service{svc, second} {
		wg.Add(1)
		go func(s *Service) {
			defer wg.Done()
			r, e := s.Command(context.Background(), f.worker, "messages.receive", in)
			results <- delivery{r, e}
		}(service)
	}
	wg.Wait()
	close(results)
	newDeliveries := 0
	for result := range results {
		if result.e != nil {
			t.Fatal(result.e)
		}
		if len(Items(result.r.Data["items"])) != 1 {
			t.Fatal("concurrent client lost the durable context")
		}
		newDeliveries += int(N(result.r.Data, "receivedCount"))
	}
	receipts, e := store.List(project.ID, "message_receipt", f.g.ID)
	if e != nil || newDeliveries != 1 || len(receipts) != 1 {
		t.Fatal("PostgreSQL duplicated delivery", e, newDeliveries, len(receipts))
	}
	must(t, svc, f.worker, "tasks.complete", M{"id": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken"), "exitCode": 1, "result": M{}})
	archive := filepath.Join(t.TempDir(), "inbox-backup.tar.gz")
	if e = svc.Backup(context.Background(), archive); e != nil {
		t.Fatal(e)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if e = Restore(context.Background(), archive, destination, ""); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(filepath.Join(destination, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	f.s = &Service{Store: restored, Root: destination}
	if e = f.s.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
	must(t, f.s, owner, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	f.worker = f.register(t, "Replacement after restore")
	f.run = must(t, f.s, f.worker, "tasks.claim", M{})
	inbox := f.receive(t)
	items := Items(inbox.Data["items"])
	if len(items) != 1 || S(Obj(items[0]), "id") != receipts[0].ID || S(Obj(Obj(Obj(items[0])["data"])["message"]), "id") != message.ID {
		t.Fatal("restored worker did not recover original context", inbox)
	}
	t.Log("PostgreSQL receipt failure rolled back; two clients created one receipt; SQLite restore replayed it to a replacement Worker")
}
