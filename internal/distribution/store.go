// Package distribution serves immutable, approved application packages on a
// separate public origin. It has no control-plane sessions or publishing API.
package distribution

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/sqliteutil"
	_ "modernc.org/sqlite"
)

type Release struct {
	ID          string `json:"id"`
	RequestHash string `json:"requestHash"`
	App         string `json:"app"`
	Target      string `json:"target"`
	Channel     string `json:"channel"`
	Version     string `json:"version"`
	Digest      string `json:"sha256"`
	Size        int64  `json:"size"`
	Filename    string `json:"filename"`
	ProjectID   string `json:"projectId"`
	GoalID      string `json:"goalId"`
	ReleaseID   string `json:"releaseId"`
	ArtifactID  string `json:"artifactId"`
	Commit      string `json:"commit"`
	Binding     string `json:"binding"`
	Signer      string `json:"osSigner,omitempty"`
	Sequence    int64  `json:"sequence"`
	PublishedAt string `json:"publishedAt"`
}

type Store struct {
	db   *sql.DB
	root string
}

var slug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
var filenamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,159}$`)

func Open(root string) (*Store, error)         { return open(root, false) }
func OpenReadOnly(root string) (*Store, error) { return open(root, true) }
func open(root string, readOnly bool) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("distribution root must be absolute")
	}
	if !readOnly {
		if err := os.MkdirAll(root, 0700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", sqliteutil.DSN(filepath.Join(root, "distribution.db"), readOnly))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if !readOnly {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS distribution_lock (id INTEGER PRIMARY KEY, value INTEGER NOT NULL);
INSERT OR IGNORE INTO distribution_lock VALUES (1,0);
CREATE TABLE IF NOT EXISTS releases (id TEXT PRIMARY KEY, app TEXT NOT NULL, target TEXT NOT NULL, channel TEXT NOT NULL, version TEXT NOT NULL, body TEXT NOT NULL, UNIQUE(app,target,channel,version));
CREATE TABLE IF NOT EXISTS channels (app TEXT NOT NULL, target TEXT NOT NULL, channel TEXT NOT NULL, release_id TEXT NOT NULL, sequence INTEGER NOT NULL, feed TEXT NOT NULL, PRIMARY KEY(app,target,channel));`)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db, root: root}, nil
}
func (s *Store) Close() error { return s.db.Close() }

func validateRelease(r Release, publicURL string, key ed25519.PrivateKey) error {
	for _, value := range []string{r.ID, r.App, r.Channel} {
		if !slug.MatchString(value) {
			return fmt.Errorf("invalid distribution identifier")
		}
	}
	if r.Target != "web" && r.Target != "windows" && r.Target != "macos" {
		return fmt.Errorf("unsupported hosted target")
	}
	if r.Target != "web" && r.Signer == "" {
		return fmt.Errorf("desktop distribution requires trusted OS signing attestation")
	}
	if !sha256Pattern.MatchString(r.Digest) || r.Size < 1 || r.Size > 512<<20 || r.RequestHash == "" {
		return fmt.Errorf("invalid approved artifact")
	}
	if !versionPattern.MatchString(r.Version) {
		return fmt.Errorf("hosted versions must use numeric major.minor.patch")
	}
	if !filenamePattern.MatchString(r.Filename) {
		return fmt.Errorf("invalid package filename")
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("public distribution URL must be an HTTPS origin")
	}
	if len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("Ed25519 update signing key required")
	}
	return nil
}

func (s *Store) Publish(ctx context.Context, r Release, source, publicURL string, key ed25519.PrivateKey) (Release, error) {
	if err := validateRelease(r, publicURL, key); err != nil {
		return r, err
	}
	if err := s.prepare(r, source); err != nil {
		return r, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE distribution_lock SET value=value+1 WHERE id=1"); err != nil {
		return r, err
	}
	if r.Target == "web" {
		var otherApp string
		err = tx.QueryRowContext(ctx, "SELECT app FROM releases WHERE target='web' AND app<>? LIMIT 1", r.App).Scan(&otherApp)
		if err == nil {
			return r, fmt.Errorf("each Web application requires its own distribution root and browser origin")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
	}
	var body string
	err = tx.QueryRowContext(ctx, "SELECT body FROM releases WHERE id=?", r.ID).Scan(&body)
	if err == nil {
		var existing Release
		if err = json.Unmarshal([]byte(body), &existing); err != nil {
			return r, err
		}
		input, stored := r, existing
		input.Sequence, stored.Sequence = 0, 0
		input.PublishedAt, stored.PublishedAt = "", ""
		x, _ := json.Marshal(input)
		y, _ := json.Marshal(stored)
		if string(x) != string(y) {
			return r, fmt.Errorf("operation key conflicts with immutable published binding")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	var currentID string
	var sequence int64
	err = tx.QueryRowContext(ctx, "SELECT release_id,sequence FROM channels WHERE app=? AND target=? AND channel=?", r.App, r.Target, r.Channel).Scan(&currentID, &sequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	if currentID != "" {
		if err = tx.QueryRowContext(ctx, "SELECT body FROM releases WHERE id=?", currentID).Scan(&body); err != nil {
			return r, err
		}
		var current Release
		if err = json.Unmarshal([]byte(body), &current); err != nil {
			return r, err
		}
		if !newerVersion(r.Version, current.Version) {
			return r, fmt.Errorf("version must advance; rollback requires a separate approved operation")
		}
	}
	r.Sequence = sequence + 1
	r.PublishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	feed, err := signFeed(r, r.Sequence, publicURL, key, time.Now())
	if err != nil {
		return r, err
	}
	b, _ := json.Marshal(r)
	signed, _ := json.Marshal(feed)
	if _, err = tx.ExecContext(ctx, "INSERT INTO releases(id,app,target,channel,version,body) VALUES(?,?,?,?,?,?)", r.ID, r.App, r.Target, r.Channel, r.Version, string(b)); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO channels(app,target,channel,release_id,sequence,feed) VALUES(?,?,?,?,?,?) ON CONFLICT(app,target,channel) DO UPDATE SET release_id=excluded.release_id,sequence=excluded.sequence,feed=excluded.feed`, r.App, r.Target, r.Channel, r.ID, r.Sequence, string(signed)); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func newerVersion(next, previous string) bool {
	a, b := strings.Split(next, "."), strings.Split(previous, ".")
	for i := range 3 {
		x, _ := strconv.ParseInt(a[i], 10, 64)
		y, _ := strconv.ParseInt(b[i], 10, 64)
		if x != y {
			return x > y
		}
	}
	return false
}

func (s *Store) Lookup(ctx context.Context, id string) (Release, error) {
	var body string
	var r Release
	err := s.db.QueryRowContext(ctx, "SELECT body FROM releases WHERE id=?", id).Scan(&body)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(body), &r)
	return r, err
}
func (s *Store) Current(ctx context.Context, app, target, channel string) (Release, error) {
	var body string
	var r Release
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT r.body,c.sequence FROM channels c JOIN releases r ON r.id=c.release_id WHERE c.app=? AND c.target=? AND c.channel=?`, app, target, channel).Scan(&body, &sequence)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(body), &r)
	r.Sequence = sequence
	return r, err
}

// Renew signs a fresh index for the current package. It never changes the
// selected version and serializes with activation using the same database lock.
func (s *Store) Renew(ctx context.Context, app, target, channel, publicURL string, key ed25519.PrivateKey) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE distribution_lock SET value=value+1 WHERE id=1"); err != nil {
		return err
	}
	var body string
	var sequence int64
	if err = tx.QueryRowContext(ctx, `SELECT r.body,c.sequence FROM channels c JOIN releases r ON r.id=c.release_id WHERE c.app=? AND c.target=? AND c.channel=?`, app, target, channel).Scan(&body, &sequence); err != nil {
		return err
	}
	var r Release
	if err = json.Unmarshal([]byte(body), &r); err != nil {
		return err
	}
	if err = validateRelease(r, publicURL, key); err != nil {
		return err
	}
	feed, err := signFeed(r, sequence+1, publicURL, key, time.Now())
	if err != nil {
		return err
	}
	b, _ := json.Marshal(feed)
	if _, err = tx.ExecContext(ctx, "UPDATE channels SET sequence=?,feed=? WHERE app=? AND target=? AND channel=?", sequence+1, string(b), app, target, channel); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) prepare(r Release, source string) error {
	stat, err := os.Lstat(source)
	if err != nil || !stat.Mode().IsRegular() {
		return fmt.Errorf("artifact must be a regular file")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	dir := filepath.Join(s.root, "blobs")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".upload-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(temp, h), io.LimitReader(input, r.Size+1))
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.Digest {
		return fmt.Errorf("approved artifact size or SHA256 mismatch")
	}
	dest := filepath.Join(dir, r.Digest)
	if err = os.Link(temp.Name(), dest); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, e := os.Open(dest)
		if e != nil {
			return e
		}
		defer existing.Close()
		h.Reset()
		size, e := io.Copy(h, existing)
		if e != nil || size != r.Size || hex.EncodeToString(h.Sum(nil)) != r.Digest {
			return fmt.Errorf("stored immutable artifact is corrupt")
		}
	}
	if directory, e := os.Open(dir); e == nil {
		_ = directory.Sync()
		directory.Close()
	}
	if r.Target == "web" {
		return s.extractWeb(r.Digest, dest)
	}
	return nil
}

func (s *Store) extractWeb(digest, source string) error {
	root := filepath.Join(s.root, "sites")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	final := filepath.Join(root, digest)
	if stat, err := os.Stat(final); err == nil && stat.IsDir() {
		return nil
	}
	dir, err := os.MkdirTemp(root, ".site-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("Web package must be a tar.gz archive: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	count := 0
	total := int64(0)
	seen := map[string]bool{}
	for {
		h, e := reader.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		count++
		total += h.Size
		if count > 20000 || total > 512<<20 || h.Size < 0 {
			return fmt.Errorf("Web archive exceeds expansion limits")
		}
		name := strings.TrimPrefix(h.Name, "./")
		name = strings.TrimSuffix(name, "/")
		if name == "" && h.Typeflag == tar.TypeDir {
			continue
		}
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00:") || seen[name] {
			return fmt.Errorf("unsafe or duplicate Web archive entry")
		}
		for _, part := range strings.Split(name, "/") {
			if strings.HasPrefix(part, ".") {
				return fmt.Errorf("hidden Web archive entries are not served")
			}
		}
		seen[name] = true
		dest := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(dest, 0700)
		case tar.TypeReg, tar.TypeRegA:
			if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return err
			}
			out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			_, err = io.Copy(out, reader)
			if err == nil {
				err = out.Sync()
			}
			closeErr := out.Close()
			if err == nil {
				err = closeErr
			}
		default:
			return fmt.Errorf("Web archives cannot contain links or special files")
		}
		if err != nil {
			return err
		}
	}
	if stat, err := os.Stat(filepath.Join(dir, "index.html")); err != nil || !stat.Mode().IsRegular() {
		return fmt.Errorf("Web archive needs a root index.html")
	}
	if err = os.Rename(dir, final); err != nil {
		if stat, e := os.Stat(final); e == nil && stat.IsDir() {
			return nil
		}
		return err
	}
	if directory, e := os.Open(root); e == nil {
		_ = directory.Sync()
		directory.Close()
	}
	return nil
}
