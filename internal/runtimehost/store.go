package runtimehost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/intelligence-substrate/platform/internal/sqliteutil"
	_ "modernc.org/sqlite"
)

type Deployment struct {
	ID             string `json:"id"`
	ReleaseID      string `json:"releaseId"`
	ArtifactID     string `json:"artifactId"`
	ArtifactDigest string `json:"artifactDigest"`
	ArtifactSize   int64  `json:"artifactSize"`
	Commit         string `json:"commit"`
	Binding        string `json:"binding"`
	ImageID        string `json:"imageId"`
	Container      string `json:"container"`
	Port           int    `json:"port"`
	Address        string `json:"address"`
	Version        string `json:"version"`
	ActivatedAt    string `json:"activatedAt"`
	PreviousID     string `json:"previousId,omitempty"`
}
type Operation struct {
	New      bool       `json:"-"`
	Key      string     `json:"key"`
	State    string     `json:"state"`
	Previous string     `json:"previous"`
	Intent   Deployment `json:"intent"`
	Result   *Result    `json:"result,omitempty"`
	Error    string     `json:"error,omitempty"`
}
type Store struct {
	db     *sql.DB
	Config Config
}

func Open(c Config, readOnly bool) (*Store, error) {
	if e := c.validate(); e != nil {
		return nil, e
	}
	if !readOnly {
		if e := os.MkdirAll(c.StateDir, 0700); e != nil {
			return nil, e
		}
	}
	db, e := sql.Open("sqlite", sqliteutil.DSN(filepath.Join(c.StateDir, "runtime.db"), readOnly))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	if !readOnly {
		for _, query := range []string{
			`CREATE TABLE IF NOT EXISTS config (id INTEGER PRIMARY KEY CHECK(id=1), owner TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS operations (key TEXT PRIMARY KEY, hash TEXT NOT NULL, state TEXT NOT NULL, previous TEXT NOT NULL DEFAULT '', intent TEXT NOT NULL DEFAULT '{}', result TEXT, error TEXT NOT NULL DEFAULT '')`,
			`CREATE TABLE IF NOT EXISTS switch_lock (id INTEGER PRIMARY KEY CHECK(id=1), operation_key TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS deployments (id TEXT PRIMARY KEY, body TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS active (id INTEGER PRIMARY KEY CHECK(id=1), deployment_id TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS observations (sequence INTEGER PRIMARY KEY AUTOINCREMENT, deployment_id TEXT NOT NULL, body TEXT NOT NULL, delivered INTEGER NOT NULL DEFAULT 0)`,
		} {
			if _, e = db.Exec(query); e != nil {
				return fail(e)
			}
		}
		if _, e = db.Exec(`INSERT INTO config(id,owner) VALUES(1,?) ON CONFLICT(id) DO NOTHING`, c.owner()); e != nil {
			return fail(e)
		}
	}
	var owner string
	if e = db.QueryRow(`SELECT owner FROM config WHERE id=1`).Scan(&owner); e != nil || owner != c.owner() {
		return fail(fmt.Errorf("runtime state belongs to a different configured application"))
	}
	return &Store{db: db, Config: c}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Lookup(ctx context.Context, key string) (Deployment, error) {
	var d Deployment
	var b string
	e := s.db.QueryRowContext(ctx, `SELECT body FROM deployments WHERE id=?`, key).Scan(&b)
	if e != nil {
		return d, e
	}
	e = json.Unmarshal([]byte(b), &d)
	return d, e
}
func (s *Store) Active(ctx context.Context) (Deployment, error) {
	var key string
	e := s.db.QueryRowContext(ctx, `SELECT deployment_id FROM active WHERE id=1`).Scan(&key)
	if e != nil {
		return Deployment{}, e
	}
	return s.Lookup(ctx, key)
}
func (s *Store) Inspect(ctx context.Context, key string) (Operation, error) {
	var op Operation
	var intent string
	var result sql.NullString
	e := s.db.QueryRowContext(ctx, `SELECT key,state,previous,intent,result,error FROM operations WHERE key=?`, key).Scan(&op.Key, &op.State, &op.Previous, &intent, &result, &op.Error)
	if e != nil {
		return op, e
	}
	if e = json.Unmarshal([]byte(intent), &op.Intent); e != nil {
		return op, e
	}
	if result.Valid {
		var r Result
		if e = json.Unmarshal([]byte(result.String), &r); e != nil {
			return op, e
		}
		op.Result = &r
	}
	return op, nil
}
func (s *Store) reserve(ctx context.Context, key, hash string) (Operation, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Operation{}, e
	}
	defer tx.Rollback()
	var recorded string
	e = tx.QueryRowContext(ctx, `SELECT hash FROM operations WHERE key=?`, key).Scan(&recorded)
	if e == nil {
		if recorded != hash {
			return Operation{}, fmt.Errorf("runtime operation conflicts with approved binding")
		}
		tx.Rollback()
		return s.Inspect(ctx, key)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Operation{}, e
	}
	var lock string
	if e = tx.QueryRowContext(ctx, `SELECT operation_key FROM switch_lock WHERE id=1`).Scan(&lock); e == nil {
		return Operation{}, fmt.Errorf("another runtime switch needs completion or explicit reconciliation")
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Operation{}, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO operations(key,hash,state) VALUES(?,?,'reserved')`, key, hash); e != nil {
		return Operation{}, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO switch_lock(id,operation_key) VALUES(1,?)`, key); e != nil {
		return Operation{}, e
	}
	return Operation{Key: key, State: "reserved", New: true}, tx.Commit()
}
func (s *Store) intent(ctx context.Context, key string, d Deployment, previous string) error {
	b, _ := json.Marshal(d)
	_, e := s.db.ExecContext(ctx, `UPDATE operations SET state='switching',intent=?,previous=? WHERE key=?`, string(b), previous, key)
	return e
}
func (s *Store) activate(ctx context.Context, d Deployment, expected string) error {
	if !hashRE.MatchString(d.ID) || d.Port < 1 || d.Port > 65535 {
		return fmt.Errorf("invalid runtime activation")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var old string
	e = tx.QueryRowContext(ctx, `SELECT deployment_id FROM active WHERE id=1`).Scan(&old)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if old != expected {
		return fmt.Errorf("active application changed since switch was approved")
	}
	d.ActivatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	d.PreviousID = old
	// Restart/reconciliation can change a container's private IP. Refresh its
	// route while preserving the original activation identity and history.
	var recorded string
	if err := tx.QueryRowContext(ctx, `SELECT body FROM deployments WHERE id=?`, d.ID).Scan(&recorded); err == nil {
		var previous Deployment
		if err = json.Unmarshal([]byte(recorded), &previous); err != nil {
			return err
		}
		d.ActivatedAt = previous.ActivatedAt
		d.PreviousID = previous.PreviousID
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	b, _ := json.Marshal(d)
	if _, e = tx.ExecContext(ctx, `INSERT INTO deployments(id,body) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body`, d.ID, string(b)); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO active(id,deployment_id) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET deployment_id=excluded.deployment_id`, d.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) finish(ctx context.Context, key string, result Result, message string) error {
	b, _ := json.Marshal(result)
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	state := "complete"
	if message != "" {
		state = "failed"
	}
	if _, e = tx.ExecContext(ctx, `UPDATE operations SET state=?,result=?,error=? WHERE key=?`, state, string(b), message, key); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM switch_lock WHERE id=1 AND operation_key=?`, key); e != nil {
		return e
	}
	return tx.Commit()
}
