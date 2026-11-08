package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB       *sql.DB
	Postgres bool
	DSN      string
}
type Tx struct{ tx *sql.Tx }
type Audit struct {
	ID         string `json:"id"`
	ProjectID  string `json:"projectId"`
	ActorID    string `json:"actorId"`
	Action     string `json:"action"`
	ResourceID string `json:"resourceId"`
	At         string `json:"at"`
	Detail     M      `json:"detail"`
	Previous   string `json:"previous"`
	Hash       string `json:"hash"`
}

func Open(dsn string) (*Store, error) {
	pg := strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
	driver := "sqlite"
	connectionDSN := dsn
	if pg {
		driver = "pgx"
	} else if dsn != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dsn), 0700); err != nil {
			return nil, err
		}
	}
	if !pg {
		separator := "?"
		if strings.Contains(connectionDSN, "?") {
			separator = "&"
		}
		connectionDSN += separator + "_txlock=immediate"
	}
	db, err := sql.Open(driver, connectionDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, Postgres: pg, DSN: dsn}
	if !pg {
		if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
			db.Close()
			return nil, err
		}
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS resources (id TEXT PRIMARY KEY, kind TEXT NOT NULL, project_id TEXT NOT NULL, goal_id TEXT NOT NULL, status TEXT NOT NULL, revision INTEGER NOT NULL, body TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS resources_project ON resources(project_id,kind,status)`,
		`CREATE INDEX IF NOT EXISTS resources_goal ON resources(goal_id,kind,status)`,
		`CREATE TABLE IF NOT EXISTS audit (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, at TEXT NOT NULL, body TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS audit_project ON audit(project_id,at)`,
		`CREATE TABLE IF NOT EXISTS identities (id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, body TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS credentials (hash TEXT PRIMARY KEY, identity_id TEXT NOT NULL, kind TEXT NOT NULL, expires_at TEXT NOT NULL, body TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS idempotency (scope TEXT NOT NULL, key TEXT NOT NULL, digest TEXT NOT NULL, resource_id TEXT NOT NULL, PRIMARY KEY(scope,key))`,
	}
	initialize := func() error {
		var executor interface {
			Exec(string, ...any) (sql.Result, error)
		} = db
		var bootstrap *sql.Tx
		if pg {
			var e error
			bootstrap, e = db.Begin()
			if e != nil {
				return e
			}
			defer bootstrap.Rollback()
			// IF NOT EXISTS does not serialize PostgreSQL catalog creation.
			// All independent initializers hold this transaction-scoped lock
			// until the entire schema is committed, including on first startup.
			if _, e = bootstrap.Exec(`SELECT pg_advisory_xact_lock(730591100)`); e != nil {
				return e
			}
			executor = bootstrap
		}
		for _, q := range statements {
			if _, e := executor.Exec(q); e != nil {
				return e
			}
		}
		if bootstrap != nil {
			return bootstrap.Commit()
		}
		return nil
	}
	if err = initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Update(ctx context.Context, fn func(*Tx) error) (err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	defer func() {
		// database/sql may finish its background rollback before the next
		// statement or Commit observes cancellation. Preserve the caller's
		// cause instead of exposing that scheduling-dependent ErrTxDone.
		if errors.Is(err, sql.ErrTxDone) && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if s.Postgres {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(730591101)`); err != nil {
			return err
		}
	}
	if err = fn(&Tx{tx}); err != nil {
		return err
	}
	return tx.Commit()
}
func get(q interface{ QueryRow(string, ...any) *sql.Row }, id string) (Resource, error) {
	var body string
	err := q.QueryRow(`SELECT body FROM resources WHERE id=$1`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, missing()
	}
	if err != nil {
		return Resource{}, err
	}
	var r Resource
	err = json.Unmarshal([]byte(body), &r)
	return r, err
}
func (t *Tx) Get(id string) (Resource, error)    { return get(t.tx, id) }
func (s *Store) Get(id string) (Resource, error) { return get(s.DB, id) }
func list(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, project, kind, goal string) ([]Resource, error) {
	query := `SELECT body FROM resources WHERE ($1='' OR project_id=$1) AND ($2='' OR kind=$2) AND ($3='' OR goal_id=$3) ORDER BY id`
	rows, err := q.Query(query, project, kind, goal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Resource{}
	for rows.Next() {
		var b string
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var r Resource
		if err = json.Unmarshal([]byte(b), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (t *Tx) List(project, kind, goal string) ([]Resource, error) {
	return list(t.tx, project, kind, goal)
}
func (s *Store) List(project, kind, goal string) ([]Resource, error) {
	return list(s.DB, project, kind, goal)
}
func (t *Tx) Put(r Resource) error {
	r.UpdatedAt = now()
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = t.tx.Exec(`INSERT INTO resources(id,kind,project_id,goal_id,status,revision,body) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET status=excluded.status,revision=excluded.revision,body=excluded.body`, r.ID, r.Kind, r.ProjectID, r.GoalID, r.Status, r.Revision, string(b))
	return e
}
func (t *Tx) Audit(a Actor, action string, r Resource, detail M) error {
	prev := ""
	var b string
	// RFC3339Nano UTC text can end at a fractional prefix: .7Z sorts after
	// .71Z. Removing only the UTC suffix restores chronological prefix order
	// without rewriting immutable historical bodies or their hashes.
	e := t.tx.QueryRow(`SELECT body FROM audit ORDER BY rtrim(at,'Z') DESC,id DESC LIMIT 1`).Scan(&b)
	if e == nil {
		var x Audit
		if e = json.Unmarshal([]byte(b), &x); e != nil {
			return e
		}
		prev = x.Hash
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	v := Audit{ID: NewID("event"), ProjectID: r.ProjectID, ActorID: a.ID, Action: action, ResourceID: r.ID, At: now(), Detail: detail, Previous: prev}
	v.Hash = Hash(v)
	body, _ := json.Marshal(v)
	_, e = t.tx.Exec(`INSERT INTO audit(id,project_id,at,body) VALUES($1,$2,$3,$4)`, v.ID, v.ProjectID, v.At, string(body))
	return e
}
func audits(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, project string) ([]Audit, error) {
	rows, e := q.Query(`SELECT body FROM audit WHERE ($1='' OR project_id=$1) ORDER BY rtrim(at,'Z'),id`, project)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Audit{}
	for rows.Next() {
		var b string
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		var v Audit
		if e = json.Unmarshal([]byte(b), &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Audits(project string) ([]Audit, error) { return audits(s.DB, project) }
func (t *Tx) Audits(project string) ([]Audit, error)    { return audits(t.tx, project) }
func (t *Tx) Once(scope, key string, payload any) (Resource, bool, error) {
	if key == "" {
		return Resource{}, false, invalid("必须提供幂等键 key")
	}
	var digest, id string
	e := t.tx.QueryRow(`SELECT digest,resource_id FROM idempotency WHERE scope=$1 AND key=$2`, scope, key).Scan(&digest, &id)
	if errors.Is(e, sql.ErrNoRows) {
		return Resource{}, false, nil
	}
	if e != nil {
		return Resource{}, false, e
	}
	if digest != Hash(payload) {
		return Resource{}, false, conflict("幂等键已用于不同的请求内容")
	}
	r, e := t.Get(id)
	return r, true, e
}
func (t *Tx) Remember(scope, key string, payload any, r Resource) error {
	_, e := t.tx.Exec(`INSERT INTO idempotency(scope,key,digest,resource_id) VALUES($1,$2,$3,$4)`, scope, key, Hash(payload), r.ID)
	return e
}

func (t *Tx) RevokeCredential(hash string) error {
	_, e := t.tx.Exec(`DELETE FROM credentials WHERE hash=$1`, hash)
	return e
}
func (t *Tx) DisableIdentity(actor Actor, id string) error {
	body, err := t.IdentityBody(id)
	if err != nil {
		return missing()
	}
	var identity M
	if err = json.Unmarshal([]byte(body), &identity); err != nil {
		return err
	}
	if S(identity, "id") != id {
		return invalid("身份记录无效")
	}
	members, err := t.List("", "member", "")
	if err != nil {
		return err
	}
	for _, member := range members {
		if S(member.Data, "identityId") != id || member.Status != "active" {
			continue
		}
		if err = t.protectLastOwner(member); err != nil {
			return err
		}
		revoked, err := t.revokeMembership(actor, member, "企业账户已停用")
		if err != nil {
			return err
		}
		if err = t.Audit(actor, "members.revoke", revoked, M{"reason": "identity_disabled", "revision": revoked.Revision}); err != nil {
			return err
		}
	}
	if !B(identity, "disabled") {
		if err = t.auditDisabledTeamIdentity(actor, id); err != nil {
			return err
		}
	}
	identity["disabled"] = true
	encoded, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	if _, e := t.tx.Exec(`UPDATE identities SET body=$1 WHERE id=$2`, string(encoded), id); e != nil {
		return e
	}
	_, e := t.tx.Exec(`DELETE FROM credentials WHERE identity_id=$1`, id)
	return e
}
func (t *Tx) BindOIDCSubject(id, subject string) error {
	if strings.TrimSpace(subject) == "" {
		return denied()
	}
	var body string
	if e := t.tx.QueryRow(`SELECT body FROM identities WHERE id=$1`, id).Scan(&body); e != nil {
		return e
	}
	var identity M
	if e := json.Unmarshal([]byte(body), &identity); e != nil {
		return e
	}
	if B(identity, "disabled") || (S(identity, "oidcSub") != "" && S(identity, "oidcSub") != subject) {
		return denied()
	}
	// Update serializes identity changes across connections. Include disabled
	// identities: disabling an account never releases its stable SSO identity.
	rows, e := t.tx.Query(`SELECT body FROM identities WHERE id<>$1`, id)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var otherBody string
		if e = rows.Scan(&otherBody); e != nil {
			return e
		}
		var other M
		if e = json.Unmarshal([]byte(otherBody), &other); e != nil {
			return e
		}
		if S(other, "oidcSub") == subject {
			return denied()
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	identity["oidcSub"] = subject
	b, _ := json.Marshal(identity)
	_, e = t.tx.Exec(`UPDATE identities SET body=$1 WHERE id=$2`, string(b), id)
	return e
}
