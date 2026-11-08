// Package updater consumes the public signed feed. It cannot publish releases,
// confer quality approval, or obtain a publisher's private credentials.
package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/distribution"
	"github.com/intelligence-substrate/platform/internal/sqliteutil"
	_ "modernc.org/sqlite"
)

type Config struct {
	StateDir         string `json:"stateDir"`
	Origin           string `json:"origin"`
	App              string `json:"app"`
	Target           string `json:"target"`
	Channel          string `json:"channel"`
	PublicKey        string `json:"publicKey"`
	ExpectedSigner   string `json:"expectedSigner"`
	PackageID        string `json:"packageId"`
	WindowsPublisher string `json:"windowsPublisher,omitempty"`
	CAFile           string `json:"caFile,omitempty"`
}

type Snapshot struct {
	State            string            `json:"state"`
	Feed             distribution.Feed `json:"feed"`
	MinimumSequence  int64             `json:"minimumSequence"`
	InstalledVersion string            `json:"installedVersion,omitempty"`
	PackagePath      string            `json:"packagePath,omitempty"`
	AttemptID        string            `json:"attemptId,omitempty"`
	Error            string            `json:"error,omitempty"`
	UpdatedAt        string            `json:"updatedAt"`
}

type record struct {
	Snapshot
	Envelope    distribution.SignedFeed `json:"envelope"`
	PayloadHash string                  `json:"payloadHash"`
}

type nativeBackend interface {
	Verify(context.Context, Config, string, distribution.Feed) error
	Installed(context.Context, Config) (string, error)
	Install(context.Context, Config, string) error
}

type Client struct {
	config Config
	key    ed25519.PublicKey
	db     *sql.DB
	http   *http.Client
	now    func() time.Time
	native nativeBackend
}

var slug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,95}$`)
var packageID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9.-]{2,199}$`)
var version = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
var filename = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,159}$`)
var hash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commit = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func Open(config Config) (*Client, error) {
	u, err := url.Parse(config.Origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return nil, fmt.Errorf("update origin must be a fixed HTTPS origin")
	}
	config.Origin = strings.TrimSuffix(config.Origin, "/")
	key, err := base64.StdEncoding.DecodeString(config.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || !slug.MatchString(config.App) || !slug.MatchString(config.Channel) || !packageID.MatchString(config.PackageID) || !filepath.IsAbs(config.StateDir) || filepath.Dir(config.StateDir) == config.StateDir {
		return nil, fmt.Errorf("fixed application, channel, package identity, public key and absolute private stateDir required")
	}
	if config.Target == "windows" {
		if !regexp.MustCompile(`^[A-Fa-f0-9]{40}$`).MatchString(config.ExpectedSigner) {
			return nil, fmt.Errorf("Windows requires a pinned signing certificate SHA1 thumbprint")
		}
		config.ExpectedSigner = strings.ToUpper(config.ExpectedSigner)
		if config.WindowsPublisher == "" || len(config.WindowsPublisher) > 1024 || strings.ContainsAny(config.WindowsPublisher, "\r\n\x00") {
			return nil, fmt.Errorf("Windows requires the fixed MSIX publisher identity")
		}
	} else if config.Target != "macos" || !regexp.MustCompile(`^[A-Z0-9]{10}$`).MatchString(config.ExpectedSigner) {
		return nil, fmt.Errorf("macOS requires a pinned Developer ID team; unsupported update target")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if config.CAFile != "" {
		if !filepath.IsAbs(config.CAFile) {
			return nil, fmt.Errorf("enterprise CA file must have an absolute administrator-owned path")
		}
		info, err := os.Lstat(config.CAFile)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 64000 {
			return nil, fmt.Errorf("invalid enterprise CA file")
		}
		body, err := os.ReadFile(config.CAFile)
		if err != nil {
			return nil, err
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(body) {
			return nil, fmt.Errorf("enterprise CA file contains no valid certificates")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	if err = os.MkdirAll(config.StateDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(config.StateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("stateDir must be a private directory, not a symlink")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("stateDir must be private (0700) to the updater's security context")
	}
	name := filepath.Join(config.StateDir, "updater.db")
	if info, err = os.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("updater database must be a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteutil.DSN(name, false))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c := &Client{config: config, key: ed25519.PublicKey(key), db: db, http: &http.Client{Transport: transport, Timeout: 3 * time.Minute}, now: time.Now, native: systemNative{}}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS updater (id INTEGER PRIMARY KEY CHECK(id=1), profile TEXT NOT NULL, body TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	encoded, _ := json.Marshal(config)
	fingerprint := sum(encoded)
	initial, _ := json.Marshal(record{Snapshot: Snapshot{State: "unchecked"}})
	if _, err = db.Exec("INSERT OR IGNORE INTO updater(id,profile,body) VALUES(1,?,?)", fingerprint, string(initial)); err != nil {
		db.Close()
		return nil, err
	}
	var stored string
	if err = db.QueryRow("SELECT profile FROM updater WHERE id=1").Scan(&stored); err != nil || stored != fingerprint {
		db.Close()
		return nil, fmt.Errorf("update trust/profile differs from durable state; explicit administrator migration required")
	}
	if err = os.Chmod(name, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error { return c.db.Close() }
func sum(b []byte) string      { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }

func readRecord(row interface{ Scan(...any) error }) (record, error) {
	var body string
	var r record
	if err := row.Scan(&body); err != nil {
		return r, err
	}
	if len(body) > 128000 {
		return r, fmt.Errorf("invalid updater state")
	}
	err := json.Unmarshal([]byte(body), &r)
	return r, err
}

func (c *Client) Status(ctx context.Context) (Snapshot, error) {
	r, err := readRecord(c.db.QueryRowContext(ctx, "SELECT body FROM updater WHERE id=1"))
	return r.Snapshot, err
}

func (c *Client) begin(ctx context.Context) (*sql.Tx, record, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, record{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE updater SET profile=profile WHERE id=1"); err != nil {
		tx.Rollback()
		return nil, record{}, err
	}
	r, err := readRecord(tx.QueryRowContext(ctx, "SELECT body FROM updater WHERE id=1"))
	if err != nil {
		tx.Rollback()
		return nil, r, err
	}
	return tx, r, nil
}

func (c *Client) save(ctx context.Context, tx *sql.Tx, r record) (Snapshot, error) {
	r.UpdatedAt = c.now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(r)
	if err != nil {
		return r.Snapshot, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE updater SET body=? WHERE id=1", string(b)); err != nil {
		return r.Snapshot, err
	}
	return r.Snapshot, tx.Commit()
}

func compare(a, b string) int {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		left, _ := strconv.Atoi(x[i])
		right, _ := strconv.Atoi(y[i])
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
	}
	return 0
}

func busy(r record) bool { return r.State == "installing" || r.State == "reconciliation_required" }
