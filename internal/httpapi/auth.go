package httpapi

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

type Identity struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	PasswordHash string `json:"passwordHash"`
	Admin        bool   `json:"admin"`
	Disabled     bool   `json:"disabled"`
	OIDCSub      string `json:"oidcSub,omitempty"`
}
type Auth struct {
	Store   *p.Store
	Service *p.Service
	Secure  bool
}
type Session struct {
	Actor p.Actor `json:"actor"`
	CSRF  string  `json:"csrf"`
}

func passwordHash(password string) string {
	salt := make([]byte, 24)
	if _, e := rand.Read(salt); e != nil {
		panic(e)
	}
	key, e := pbkdf2.Key(sha256.New, password, salt, 210000, 32)
	if e != nil {
		panic(e)
	}
	return hex.EncodeToString(salt) + ":" + hex.EncodeToString(key)
}
func checkPassword(encoded, password string) bool {
	parts := strings.Split(encoded, ":")
	if len(parts) != 2 {
		return false
	}
	salt, e := hex.DecodeString(parts[0])
	if e != nil {
		return false
	}
	expected, e := hex.DecodeString(parts[1])
	if e != nil {
		return false
	}
	key, e := pbkdf2.Key(sha256.New, password, salt, 210000, 32)
	return e == nil && subtle.ConstantTimeCompare(key, expected) == 1
}
func (a *Auth) CreateIdentity(email, name, password string, admin bool) (Identity, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, e := mail.ParseAddress(email)
	if e != nil || address.Address != email || len(password) < 14 || len(password) > 256 || strings.TrimSpace(name) == "" {
		return Identity{}, &p.Fault{Code: "invalid", Message: "需要有效邮箱、姓名和至少 14 位密码", Status: 422}
	}
	id := Identity{ID: p.NewID("user"), Name: name, Email: email, PasswordHash: passwordHash(password), Admin: admin}
	body, _ := json.Marshal(id)
	_, e = a.Store.DB.Exec(`INSERT INTO identities(id,email,body) VALUES($1,$2,$3)`, id.ID, email, string(body))
	return id, e
}
func (a *Auth) IdentityByEmail(email string) (Identity, error) {
	var body string
	e := a.Store.DB.QueryRow(`SELECT body FROM identities WHERE email=$1`, strings.ToLower(strings.TrimSpace(email))).Scan(&body)
	if e != nil {
		return Identity{}, e
	}
	var u Identity
	e = json.Unmarshal([]byte(body), &u)
	return u, e
}
func (a *Auth) identity(id string) (Identity, error) {
	var b string
	e := a.Store.DB.QueryRow(`SELECT body FROM identities WHERE id=$1`, id).Scan(&b)
	if e != nil {
		return Identity{}, e
	}
	var u Identity
	e = json.Unmarshal([]byte(b), &u)
	return u, e
}
func identityActor(u Identity) p.Actor {
	return p.Actor{ID: u.ID, Name: u.Name, Kind: "human", Admin: u.Admin}
}
func (a *Auth) Bootstrap(email, password string) error {
	var n int
	if e := a.Store.DB.QueryRow(`SELECT COUNT(*) FROM identities`).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return nil
	}
	_, e := a.CreateIdentity(email, "平台管理员", password, true)
	return e
}
func (a *Auth) newSession(u Identity) (string, Session, error) {
	token := p.Secret()
	var session Session
	err := a.Store.Update(context.Background(), func(t *p.Tx) error {
		body, err := t.IdentityBody(u.ID)
		if err != nil {
			return err
		}
		var current Identity
		if err = json.Unmarshal([]byte(body), &current); err != nil {
			return err
		}
		if current.ID != u.ID || current.Disabled {
			return &p.Fault{Code: "unauthorized", Message: "账户已停用", Status: 401}
		}
		session = Session{Actor: identityActor(current), CSRF: p.Secret()}
		encoded, _ := json.Marshal(session)
		return t.InsertCredential(p.Hash(token), p.Credential{IdentityID: u.ID, Kind: "session", ExpiresAt: time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339Nano), Body: string(encoded)})
	})
	if err != nil {
		return "", Session{}, err
	}
	return token, session, nil
}
func (a *Auth) Login(email, password string) (string, Session, error) {
	u, e := a.IdentityByEmail(email)
	if e != nil || u.Disabled || !checkPassword(u.PasswordHash, password) {
		return "", Session{}, &p.Fault{Code: "unauthorized", Message: "邮箱或密码不正确", Status: 401}
	}
	return a.newSession(u)
}
func (a *Auth) Authenticate(r *http.Request) (p.Actor, string, bool, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	bearer := r.Header.Get("Authorization") != ""
	if _, pass, ok := r.BasicAuth(); ok {
		token = pass
		bearer = true
	}
	if !bearer {
		cookie, err := r.Cookie("is_session")
		if err != nil {
			return p.Actor{}, "", false, err
		}
		token = cookie.Value
	}
	var actor p.Actor
	var csrf string
	err := a.Store.Update(r.Context(), func(t *p.Tx) error {
		credential, err := t.Credential(p.Hash(token))
		if err != nil {
			return errors.New("invalid credential")
		}
		if (bearer && credential.Kind != "token") || (!bearer && credential.Kind != "session") {
			return errors.New("invalid credential kind")
		}
		if credential.Kind == "session" {
			var session Session
			if err = json.Unmarshal([]byte(credential.Body), &session); err != nil {
				return err
			}
			actor, csrf = session.Actor, session.CSRF
		} else if err = json.Unmarshal([]byte(credential.Body), &actor); err != nil {
			return err
		}
		actor.CredentialHash = p.Hash(token)
		actor, err = t.CurrentActor(actor)
		return err
	})
	if err != nil {
		return p.Actor{}, "", bearer, err
	}
	return actor, csrf, bearer, nil
}
func (a *Auth) IssueToken(ctx context.Context, issuer p.Actor, in p.M) (string, p.Actor, error) {
	token := p.Secret()
	var actor p.Actor
	err := a.Store.Update(ctx, func(t *p.Tx) error {
		current, err := t.CurrentActor(issuer)
		if err != nil {
			return err
		}
		issuer = current
		if issuer.Kind != "human" {
			return &p.Fault{Code: "forbidden", Message: "需要人工授权凭据", Status: 403}
		}
		// Trusted in-process callers also require a current registered issuer.
		body, err := t.IdentityBody(issuer.ID)
		if err != nil {
			return &p.Fault{Code: "forbidden", Message: "授权账户不存在", Status: 403}
		}
		var identity Identity
		if err = json.Unmarshal([]byte(body), &identity); err != nil {
			return err
		}
		if identity.ID != issuer.ID || identity.Disabled {
			return &p.Fault{Code: "forbidden", Message: "授权账户已停用", Status: 403}
		}
		issuer.Admin = identity.Admin
		project := p.S(in, "projectId")
		if err = t.RequireProjectAccess(issuer, project, "owner"); err != nil {
			return err
		}
		resource, err := t.Get(project)
		if err != nil || resource.Kind != "project" {
			return &p.Fault{Code: "invalid", Message: "项目不存在", Status: 422}
		}
		actor = p.Actor{ID: p.NewID("agent"), Name: p.S(in, "name"), Kind: "agent", ProjectID: project, Roles: []string{"developer"}}
		if workerID := p.S(in, "workerId"); workerID != "" {
			worker, err := t.Get(workerID)
			if err != nil || worker.Kind != "worker" || worker.ProjectID != project || worker.Status == "disabled" {
				return &p.Fault{Code: "invalid", Message: "执行节点不存在或已停用", Status: 422}
			}
			actor.ID, actor.Name, actor.Kind, actor.WorkerID, actor.Roles = "worker:"+worker.ID, worker.Title, "worker", worker.ID, p.Strings(worker.Data["roles"])
		}
		encoded, _ := json.Marshal(actor)
		if err = t.InsertCredential(p.Hash(token), p.Credential{IdentityID: issuer.ID, Kind: "token", ExpiresAt: time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339Nano), Body: string(encoded)}); err != nil {
			return err
		}
		return t.Audit(issuer, "tokens.issue", p.Resource{ID: actor.ID, ProjectID: project}, p.M{"workerId": actor.WorkerID})
	})
	if err != nil {
		return "", p.Actor{}, err
	}
	return token, actor, nil
}
func (a *Auth) Logout(r *http.Request) {
	if c, e := r.Cookie("is_session"); e == nil {
		_, _ = a.Store.DB.Exec(`DELETE FROM credentials WHERE hash=$1`, p.Hash(c.Value))
	}
}
func (a *Auth) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: "is_session", Value: token, Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
}
func (a *Auth) Identities() ([]p.Actor, error) {
	rows, e := a.Store.DB.Query(`SELECT body FROM identities ORDER BY email`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []p.Actor{}
	for rows.Next() {
		var b string
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		var u Identity
		if e = json.Unmarshal([]byte(b), &u); e != nil {
			return nil, e
		}
		actor := identityActor(u)
		actor.Name = u.Name + " <" + u.Email + ">"
		out = append(out, actor)
	}
	return out, rows.Err()
}

func (a *Auth) Tokens(ctx context.Context, actor p.Actor, project string) ([]p.M, error) {
	var tokens []p.M
	err := a.Store.Update(ctx, func(t *p.Tx) error {
		current, err := t.CurrentActor(actor)
		if err != nil {
			return err
		}
		if current.Kind != "human" {
			return &p.Fault{Code: "forbidden", Message: "需要项目负责人", Status: 403}
		}
		if err = t.RequireProjectAccess(current, project, "owner"); err != nil {
			return err
		}
		tokens, err = t.ProjectTokens(project)
		return err
	})
	return tokens, err
}
func (a *Auth) Revoke(ctx context.Context, actor p.Actor, in p.M) error {
	return a.Store.Update(ctx, func(t *p.Tx) error {
		current, err := t.CurrentActor(actor)
		if err != nil {
			return err
		}
		if current.Kind != "human" {
			return &p.Fault{Code: "forbidden", Message: "需要项目负责人", Status: 403}
		}
		project, hash := p.S(in, "projectId"), p.S(in, "id")
		if err = t.RequireProjectAccess(current, project, "owner"); err != nil {
			return err
		}
		credential, err := t.Credential(hash)
		if err != nil || credential.Kind != "token" {
			return &p.Fault{Code: "not_found", Message: "凭据不存在", Status: 404}
		}
		var delegated p.Actor
		if err = json.Unmarshal([]byte(credential.Body), &delegated); err != nil {
			return err
		}
		if delegated.ProjectID != project {
			return &p.Fault{Code: "not_found", Message: "凭据不存在", Status: 404}
		}
		if err = t.RevokeCredential(hash); err != nil {
			return err
		}
		return t.Audit(current, "tokens.revoke", p.Resource{ID: hash[:min(12, len(hash))], ProjectID: project}, p.M{})
	})
}
func (a *Auth) Disable(ctx context.Context, actor p.Actor, id string) error {
	return a.Store.Update(ctx, func(t *p.Tx) error {
		current, err := t.CurrentActor(actor)
		if err != nil {
			return err
		}
		if !current.Admin || current.Kind != "human" || current.ID == id {
			return &p.Fault{Code: "forbidden", Message: "管理员不能通过此操作停用自己", Status: 403}
		}
		if err = t.DisableIdentity(current, id); err != nil {
			return err
		}
		return t.Audit(current, "identities.disable", p.Resource{ID: id}, p.M{})
	})
}
