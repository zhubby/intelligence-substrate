// Package publisher runs only in a trusted release controller. Destination and
// credential choices are administrator-owned; repository code cannot supply them.
package publisher

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/intelligence-substrate/platform/internal/sqliteutil"
	"golang.org/x/oauth2"
)

type Config struct {
	StateDir string    `json:"stateDir"`
	Profiles []Profile `json:"profiles"`
}
type Profile struct {
	ProjectID         string   `json:"projectId"`
	GoalID            string   `json:"goalId"`
	Target            string   `json:"target"`
	Driver            string   `json:"driver"`
	Environment       string   `json:"environment,omitempty"`
	AppID             string   `json:"appId"`
	Channel           string   `json:"channel,omitempty"`
	DistributionRoot  string   `json:"distributionRoot,omitempty"`
	PublicURL         string   `json:"publicUrl,omitempty"`
	SigningKeyFile    string   `json:"signingKeyFile,omitempty"`
	InstallerFilename string   `json:"installerFilename,omitempty"`
	ExpectedSigner    string   `json:"expectedSigner,omitempty"`
	Track             string   `json:"track,omitempty"`
	CredentialsFile   string   `json:"credentialsFile,omitempty"`
	BundleID          string   `json:"bundleId,omitempty"`
	UploadHosts       []string `json:"uploadHosts,omitempty"`
	AAPTPath          string   `json:"aaptPath,omitempty"`
	APKSignerPath     string   `json:"apkSignerPath,omitempty"`
	ChineseMainland   *bool    `json:"chineseMainland,omitempty"`
	MetadataFile      string   `json:"metadataFile,omitempty"`
	MetadataDigest    string   `json:"metadataDigest,omitempty"`
}
type Request struct {
	ProtocolVersion      string `json:"protocolVersion"`
	Action               string `json:"action,omitempty"`
	OperationKey         string `json:"operationKey"`
	RunID                string `json:"runId,omitempty"`
	ProjectID            string `json:"projectId"`
	GoalID               string `json:"goalId"`
	ReleaseID            string `json:"releaseId"`
	Target               string `json:"target"`
	Version              string `json:"version"`
	ArtifactID           string `json:"artifactId"`
	ArtifactPath         string `json:"artifactPath,omitempty"`
	ArtifactDigest       string `json:"artifactDigest"`
	ArtifactSize         int64  `json:"artifactSize"`
	Commit               string `json:"commit"`
	Binding              string `json:"binding"`
	ExternalID           string `json:"externalId,omitempty"`
	MetadataDigest       string `json:"metadataDigest,omitempty"`
	MaterialRevisionID   string `json:"materialRevisionId,omitempty"`
	SourceOperationKey   string `json:"sourceOperationKey,omitempty"`
	SourceBinding        string `json:"sourceBinding,omitempty"`
	SourceMetadataDigest string `json:"sourceMetadataDigest,omitempty"`
	MaterialScope        string `json:"materialScope,omitempty"`
}
type Result struct {
	ExternalID       string `json:"externalId"`
	State            string `json:"state"`
	UpstreamState    string `json:"upstreamState,omitempty"`
	Reason           string `json:"reason,omitempty"`
	URL              string `json:"url,omitempty"`
	ContinueRequired bool   `json:"continueRequired,omitempty"`
}
type Receipt struct {
	EditID                string                 `json:"editId,omitempty"`
	VersionCode           string                 `json:"versionCode,omitempty"`
	Apple                 *appleReceipt          `json:"apple,omitempty"`
	Huawei                *huaweiReceipt         `json:"huawei,omitempty"`
	Xiaomi                *xiaomiReceipt         `json:"xiaomi,omitempty"`
	Oppo                  *oppoReceipt           `json:"oppo,omitempty"`
	Vivo                  *vivoReceipt           `json:"vivo,omitempty"`
	GoogleReconciliations []GoogleReconciliation `json:"googleReconciliations,omitempty"`
}
type Operation struct {
	OperationKey         string                `json:"operationKey"`
	JournalDigest        string                `json:"journalDigest"`
	State                string                `json:"state"`
	Receipt              Receipt               `json:"receipt"`
	Result               *Result               `json:"result,omitempty"`
	StoreReconciliations []StoreReconciliation `json:"storeReconciliations,omitempty"`
	GoogleReservation    *GoogleReservation    `json:"googleReservation,omitempty"`
	GoogleQuery          *GoogleQuery          `json:"googleQuery,omitempty"`
}
type Engine struct {
	Config               Config
	db                   *sql.DB
	apiBase              string
	HTTP                 *http.Client
	tokenSource          func(context.Context, Profile) (oauth2.TokenSource, error)
	appleBase            string
	verifyApplePackage   func(context.Context, Profile, Request, string) (applePackage, error)
	verifyAndroidPackage func(context.Context, Profile, Request, string) (androidPackage, error)
	huaweiNow            func() time.Time
}

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40,64}$`)
var identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,95}$`)
var packagePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)
var hostedIdentifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)

func Open(config Config) (*Engine, error) {
	if !filepath.IsAbs(config.StateDir) || len(config.Profiles) == 0 {
		return nil, fmt.Errorf("absolute publisher stateDir and fixed project/goal profiles required")
	}
	seen := map[string]bool{}
	destinations := map[string]bool{}
	webOrigins := map[string]bool{}
	for _, p := range config.Profiles {
		if !identifierPattern.MatchString(p.ProjectID) || !identifierPattern.MatchString(p.GoalID) {
			return nil, fmt.Errorf("publisher profiles require exact projectId and goalId")
		}
		key := p.ProjectID + ":" + p.GoalID + ":" + p.Target
		if seen[key] {
			return nil, fmt.Errorf("duplicate publishing profile")
		}
		seen[key] = true
		if p.Driver != "vivo-store" && p.Environment != "" {
			return nil, fmt.Errorf("explicit environment selector is only supported by vivo profiles")
		}
		if p.Driver != "app-store" && p.Driver != "xiaomi-market" && p.Driver != "oppo-store" && p.Driver != "vivo-store" && (p.MetadataFile != "" || p.MetadataDigest != "") {
			return nil, fmt.Errorf("reviewed metadata selectors are only supported by App Store, Xiaomi, OPPO and vivo profiles")
		}
		switch p.Driver {
		case "hosted":
			if p.Target != "web" && p.Target != "windows" && p.Target != "macos" {
				return nil, fmt.Errorf("unsupported hosted target")
			}
			if !filepath.IsAbs(p.DistributionRoot) || !filepath.IsAbs(p.SigningKeyFile) || !hostedIdentifierPattern.MatchString(p.AppID) || !hostedIdentifierPattern.MatchString(p.Channel) {
				return nil, fmt.Errorf("hosted profile needs fixed application, channel, root and signing key path")
			}
			u, err := url.Parse(p.PublicURL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
				return nil, fmt.Errorf("hosted publicUrl must be a dedicated HTTPS origin")
			}
			if p.Target == "web" {
				port := u.Port()
				if port == "" {
					port = "443"
				}
				origin := strings.ToLower(u.Hostname()) + ":" + port
				if webOrigins[origin] {
					return nil, fmt.Errorf("each generated Web application needs a separate browser origin")
				}
				webOrigins[origin] = true
			}
			if withinDirectory(p.DistributionRoot, p.SigningKeyFile) {
				return nil, fmt.Errorf("private signing keys must remain outside the public service volume")
			}
			if p.Target == "web" && p.Channel != "stable" {
				return nil, fmt.Errorf("hosted Web currently serves only the stable channel")
			}
			if p.Target != "web" && (p.ExpectedSigner == "" || p.InstallerFilename == "") {
				return nil, fmt.Errorf("desktop distribution requires installer filename and trusted OS signer")
			}
		case "google-play":
			if p.Target != "android-google-play" || !packagePattern.MatchString(p.AppID) || !identifierPattern.MatchString(p.Track) || !filepath.IsAbs(p.CredentialsFile) {
				return nil, fmt.Errorf("Google Play profile needs package name, track and credential file path")
			}
		case "app-store":
			if err := validateAppleProfile(p); err != nil {
				return nil, err
			}
		case "huawei-appgallery":
			if err := validateHuaweiProfile(p); err != nil {
				return nil, err
			}
		case "xiaomi-market":
			if err := validateXiaomiProfile(p); err != nil {
				return nil, err
			}
		case "oppo-store":
			if err := validateOppoProfile(p); err != nil {
				return nil, err
			}
		case "vivo-store":
			if err := validateVivoProfile(p); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported publisher driver %q", p.Driver)
		}
		destination := p.Driver + ":" + p.DistributionRoot + ":" + p.AppID + ":" + p.Target + ":" + p.Channel + ":" + p.Track + ":" + p.Environment
		if destinations[destination] {
			return nil, fmt.Errorf("multiple goals cannot share a publishing destination")
		}
		destinations[destination] = true
	}
	for _, public := range config.Profiles {
		if public.Driver == "hosted" {
			for _, profile := range config.Profiles {
				for _, name := range []string{profile.SigningKeyFile, profile.CredentialsFile, profile.MetadataFile} {
					if name != "" && withinDirectory(public.DistributionRoot, name) {
						return nil, fmt.Errorf("private signing and channel credentials must remain outside all public service volumes")
					}
				}
			}
		}
	}
	if err := os.MkdirAll(config.StateDir, 0700); err != nil {
		return nil, err
	}
	for _, p := range config.Profiles {
		if p.Driver == "oppo-store" {
			if err := prepareOppoState(config.StateDir); err != nil {
				return nil, err
			}
			break
		}
	}
	// New journals may retain private portal attachments. Preserve permissions
	// on existing databases; reconciliation refuses unsafe evidence storage.
	stateFile, createErr := os.OpenFile(filepath.Join(config.StateDir, "publisher.db"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if createErr == nil {
		if err := stateFile.Close(); err != nil {
			return nil, err
		}
	} else if !os.IsExist(createErr) {
		return nil, createErr
	}
	db, err := sql.Open("sqlite", sqliteutil.DSN(filepath.Join(config.StateDir, "publisher.db"), false))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS operations (operation_key TEXT PRIMARY KEY, request_hash TEXT NOT NULL, state TEXT NOT NULL, receipt TEXT NOT NULL, result TEXT, updated_at TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS apple_destinations (app_id TEXT PRIMARY KEY, operation_key TEXT NOT NULL UNIQUE);
	CREATE TABLE IF NOT EXISTS apple_versions (app_id TEXT NOT NULL, version TEXT NOT NULL, build_number TEXT NOT NULL, operation_key TEXT NOT NULL UNIQUE, PRIMARY KEY(app_id,version,build_number));
	CREATE TABLE IF NOT EXISTS apple_material_revisions (parent_operation_key TEXT PRIMARY KEY, operation_key TEXT NOT NULL UNIQUE);
	CREATE TABLE IF NOT EXISTS huawei_destinations (app_id TEXT PRIMARY KEY, operation_key TEXT NOT NULL UNIQUE);
	CREATE TABLE IF NOT EXISTS xiaomi_destinations (package_name TEXT PRIMARY KEY, operation_key TEXT NOT NULL UNIQUE);
	CREATE TABLE IF NOT EXISTS oppo_destinations (package_name TEXT PRIMARY KEY, operation_key TEXT NOT NULL UNIQUE);
	CREATE TABLE IF NOT EXISTS oppo_versions (package_name TEXT NOT NULL, version_code INTEGER NOT NULL, operation_key TEXT NOT NULL UNIQUE, PRIMARY KEY(package_name,version_code));
	CREATE TABLE IF NOT EXISTS vivo_destinations (environment TEXT NOT NULL, package_name TEXT NOT NULL, operation_key TEXT NOT NULL UNIQUE, PRIMARY KEY(environment,package_name));
	CREATE TABLE IF NOT EXISTS vivo_versions (environment TEXT NOT NULL, package_name TEXT NOT NULL, version_code INTEGER NOT NULL, operation_key TEXT NOT NULL UNIQUE, PRIMARY KEY(environment,package_name,version_code));
	CREATE TABLE IF NOT EXISTS oppo_tokens (client_id TEXT PRIMARY KEY, credential_hash TEXT NOT NULL, access_token TEXT NOT NULL, expires_at INTEGER NOT NULL);`); err != nil {
		db.Close()
		return nil, err
	}
	engine := &Engine{Config: config, db: db, apiBase: "https://androidpublisher.googleapis.com", HTTP: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err = initializeStoreRecovery(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = initializeGooglePublicationJournal(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = initializeGoogleQueryJournal(db); err != nil {
		db.Close()
		return nil, err
	}
	engine.tokenSource = engine.googleTokenSource
	engine.appleBase = "https://api.appstoreconnect.apple.com"
	engine.verifyApplePackage = verifyApplePackage
	engine.verifyAndroidPackage = verifyAndroidPackage
	engine.huaweiNow = time.Now
	return engine, nil
}

func withinDirectory(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	realRoot, e1 := filepath.EvalSymlinks(root)
	realName, e2 := filepath.EvalSymlinks(name)
	if e1 == nil && e2 == nil {
		rel, err = filepath.Rel(realRoot, realName)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return false
}
func (e *Engine) Close() error { return e.db.Close() }
func (e *Engine) Inspect(ctx context.Context, key string) (Operation, error) {
	j, err := e.inspectJournal(ctx, key)
	return j.Operation, err
}
func (e *Engine) checkpoint(ctx context.Context, key string, receipt Receipt) error {
	b, _ := json.Marshal(receipt)
	_, err := e.db.ExecContext(ctx, "UPDATE operations SET receipt=?,updated_at=? WHERE operation_key=?", string(b), time.Now().UTC().Format(time.RFC3339Nano), key)
	return err
}
func (e *Engine) finish(ctx context.Context, key string, result Result) error {
	b, _ := json.Marshal(result)
	_, err := e.db.ExecContext(ctx, "UPDATE operations SET state='complete',result=?,updated_at=? WHERE operation_key=?", string(b), time.Now().UTC().Format(time.RFC3339Nano), key)
	return err
}

func (e *Engine) profile(r Request) (Profile, error) {
	for _, p := range e.Config.Profiles {
		if p.ProjectID == r.ProjectID && p.GoalID == r.GoalID && p.Target == r.Target {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("no administrator-approved destination for this project, goal and target")
}
func requestHash(r Request, p Profile) string {
	r.RunID, r.ArtifactPath, r.Action, r.ExternalID = "", "", "", ""
	// Credential/key rotation does not grant a new destination or operation.
	p.CredentialsFile, p.SigningKeyFile = "", ""
	b, _ := json.Marshal(struct {
		Request Request
		Profile Profile
	}{r, p})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validateRequest(r Request) error {
	if r.ProtocolVersion != "1" || (r.Action != "" && r.Action != "publish" && r.Action != "status" && r.Action != "continue" && r.Action != "materials") {
		return fmt.Errorf("unsupported trusted publisher protocol or action")
	}
	if !hashPattern.MatchString(r.OperationKey) || !hashPattern.MatchString(r.ArtifactDigest) || !hashPattern.MatchString(r.Binding) || !commitPattern.MatchString(r.Commit) || r.ArtifactSize < 1 || r.ArtifactSize > 512<<20 {
		return fmt.Errorf("complete approved binding, artifact digest and size required")
	}
	for _, value := range []string{r.ProjectID, r.GoalID, r.ReleaseID, r.ArtifactID} {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("invalid publication identity")
		}
	}
	if len(r.Version) == 0 || len(r.Version) > 80 {
		return fmt.Errorf("version required")
	}
	if r.Action == "materials" || r.MaterialRevisionID != "" || r.SourceOperationKey != "" || r.SourceBinding != "" || r.SourceMetadataDigest != "" || r.MaterialScope != "" {
		if !identifierPattern.MatchString(r.MaterialRevisionID) || !hashPattern.MatchString(r.SourceOperationKey) || !hashPattern.MatchString(r.SourceBinding) || !hashPattern.MatchString(r.SourceMetadataDigest) || !hashPattern.MatchString(r.MetadataDigest) || r.OperationKey == r.SourceOperationKey || r.Binding == r.SourceBinding || r.MetadataDigest == r.SourceMetadataDigest || r.ExternalID == "" || r.Target != "ios-appstore" || (r.MaterialScope != "" && r.MaterialScope != "screenshots") || (r.Action != "materials" && r.Action != "status" && !(r.Action == "continue" && r.MaterialScope == "screenshots")) {
			return fmt.Errorf("material revision requires a new approval, exact source and scoped materials/status/continue action")
		}
	}
	return nil
}

func (e *Engine) Execute(ctx context.Context, r Request) (Result, error) {
	if err := validateRequest(r); err != nil {
		return Result{}, err
	}
	p, err := e.profile(r)
	if err != nil {
		return Result{}, err
	}
	hash := requestHash(r, p)
	if p.Driver == "app-store" {
		if r.MaterialRevisionID != "" {
			return e.executeAppleMaterialRevision(ctx, p, r, hash)
		}
		return e.executeApple(ctx, p, r, hash)
	}
	if r.Action == "materials" || r.MaterialRevisionID != "" {
		return Result{}, fmt.Errorf("material revisions currently require the App Store driver")
	}
	if p.Driver == "huawei-appgallery" {
		return e.executeHuawei(ctx, p, r, hash)
	}
	if p.Driver == "xiaomi-market" {
		return e.executeXiaomi(ctx, p, r, hash)
	}
	if p.Driver == "oppo-store" {
		return e.executeOppo(ctx, p, r, hash)
	}
	if p.Driver == "vivo-store" {
		return e.executeVivo(ctx, p, r, hash)
	}
	if p.Driver == "google-play" {
		return e.executeGoogle(ctx, p, r, hash)
	}
	if r.Action == "continue" {
		return Result{}, fmt.Errorf("continuation is unsupported for this driver")
	}
	if r.Action == "status" {
		if r.ExternalID == "" {
			return Result{}, fmt.Errorf("known external version required for a read-only query")
		}
		var recorded string
		if err = e.db.QueryRowContext(ctx, "SELECT request_hash FROM operations WHERE operation_key=?", r.OperationKey).Scan(&recorded); err != nil || recorded != hash {
			return Result{}, fmt.Errorf("query does not match this publisher's persisted approved operation")
		}
		op, err := e.Inspect(ctx, r.OperationKey)
		if err != nil {
			return Result{}, err
		}
		if op.Result != nil && op.Result.ExternalID != r.ExternalID {
			return Result{}, fmt.Errorf("query external ID differs from persisted receipt")
		}
		if p.Driver == "hosted" {
			return e.hostedStatus(ctx, p, r)
		}
		return e.googleStatus(ctx, p, r)
	}
	staged, cleanup, err := stageArtifact(r)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	// Resolve deterministic preconditions before reserving an external operation.
	var signingKey ed25519.PrivateKey
	var signer string
	if p.Driver == "hosted" {
		if signingKey, err = LoadSigningKey(p.SigningKeyFile); err != nil {
			return Result{}, err
		}
		if p.Target != "web" {
			if signer, err = verifyDesktop(ctx, p, staged); err != nil {
				return Result{}, err
			}
		}
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	var previousHash, state string
	var previousResult sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT request_hash,state,result FROM operations WHERE operation_key=?", r.OperationKey).Scan(&previousHash, &state, &previousResult)
	if err == nil {
		if previousHash != hash {
			return Result{}, fmt.Errorf("operation key conflicts with approved binding or publishing destination")
		}
		if state == "complete" && previousResult.Valid {
			var result Result
			err = json.Unmarshal([]byte(previousResult.String), &result)
			return result, err
		}
		if p.Driver != "hosted" {
			return Result{}, fmt.Errorf("external result is uncertain; inspect persisted operation and reconcile without resubmitting")
		}
		// The hosted driver reconciles its own immutable transaction, without
		// reactivating the package or calling a remote publishing API.
		if err = tx.Rollback(); err != nil {
			return Result{}, err
		}
		result, lookupErr := e.hostedStatus(ctx, p, Request{OperationKey: r.OperationKey, ExternalID: hostedID(p, r.OperationKey)})
		if lookupErr != nil || result.State != "published" {
			return Result{}, fmt.Errorf("hosted operation interrupted before a confirmed activation; inspect and reconcile")
		}
		return result, e.finish(ctx, r.OperationKey, result)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(operation_key,request_hash,state,receipt,updated_at) VALUES(?,?,'pending','{}',?)", r.OperationKey, hash, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	var result Result
	if p.Driver == "hosted" {
		result, err = e.hostedPublish(ctx, p, r, hash, staged, signer, signingKey)
	} else {
		result, err = e.googlePublish(ctx, p, r, staged)
	}
	if err != nil {
		return Result{}, err
	}
	if err = e.finish(ctx, r.OperationKey, result); err != nil {
		return Result{}, fmt.Errorf("external operation completed but journal could not be confirmed; reconcile before further action")
	}
	return result, nil
}

func stageArtifact(r Request) (string, func(), error) {
	noop := func() {}
	stat, err := os.Lstat(r.ArtifactPath)
	if err != nil || !stat.Mode().IsRegular() {
		return "", noop, fmt.Errorf("approved artifact must be a regular file")
	}
	dir, err := os.MkdirTemp("", "is-approved-")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	name := filepath.Join(dir, "package")
	in, err := os.Open(r.ArtifactPath)
	if err != nil {
		cleanup()
		return "", noop, err
	}
	defer in.Close()
	out, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		cleanup()
		return "", noop, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, r.ArtifactSize+1))
	closeErr := out.Close()
	if err != nil || closeErr != nil || n != r.ArtifactSize || hex.EncodeToString(h.Sum(nil)) != r.ArtifactDigest {
		cleanup()
		return "", noop, fmt.Errorf("approved artifact SHA256 or size mismatch")
	}
	return name, cleanup, nil
}
func LoadSigningKey(name string) (ed25519.PrivateKey, error) {
	body, err := readPrivateFile(name)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing key must contain one base64 Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
func readPrivateFile(name string) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("cannot read administrator credential file")
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 65536 {
		return nil, fmt.Errorf("credential file is invalid")
	}
	b, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(b) > 65536 {
		return nil, fmt.Errorf("credential file exceeds limit")
	}
	return b, nil
}
