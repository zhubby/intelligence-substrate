package platform

import (
	"encoding/json"
	"time"
)

type Credential struct {
	IdentityID string
	Kind       string
	ExpiresAt  string
	Body       string
}

func (t *Tx) Credential(hash string) (Credential, error) {
	var c Credential
	err := t.tx.QueryRow(`SELECT identity_id,kind,expires_at,body FROM credentials WHERE hash=$1`, hash).Scan(&c.IdentityID, &c.Kind, &c.ExpiresAt, &c.Body)
	return c, err
}

func (t *Tx) InsertCredential(hash string, c Credential) error {
	_, err := t.tx.Exec(`INSERT INTO credentials(hash,identity_id,kind,expires_at,body) VALUES($1,$2,$3,$4,$5)`, hash, c.IdentityID, c.Kind, c.ExpiresAt, c.Body)
	return err
}

// CurrentActor rechecks authority within a governed transaction. Actors without
// a credential reference are the existing trusted in-process CLI/service API;
// HTTP requests can only obtain a reference through Auth.Authenticate.
func (t *Tx) CurrentActor(a Actor) (Actor, error) {
	if a.CredentialHash == "" {
		return a, nil
	}
	c, err := t.Credential(a.CredentialHash)
	if err != nil {
		return Actor{}, denied()
	}
	expires, err := time.Parse(time.RFC3339Nano, c.ExpiresAt)
	if err != nil || !expires.After(time.Now()) {
		return Actor{}, denied()
	}
	identity, err := t.identityState(c.IdentityID)
	if err != nil || identity.Disabled {
		return Actor{}, denied()
	}
	var stored Actor
	switch c.Kind {
	case "session":
		var session struct {
			Actor Actor `json:"actor"`
		}
		if err = json.Unmarshal([]byte(c.Body), &session); err != nil {
			return Actor{}, denied()
		}
		stored = session.Actor
		if stored.ID != identity.ID || stored.Kind != "human" || stored.ProjectID != "" || stored.WorkerID != "" {
			return Actor{}, denied()
		}
	case "token":
		if err = json.Unmarshal([]byte(c.Body), &stored); err != nil {
			return Actor{}, denied()
		}
		if stored.ID == "" || stored.ProjectID == "" || (stored.Kind != "agent" && stored.Kind != "worker") {
			return Actor{}, denied()
		}
		project, err := t.Get(stored.ProjectID)
		if err != nil || project.Kind != "project" {
			return Actor{}, denied()
		}
		issuer := Actor{ID: identity.ID, Kind: "human", Admin: identity.Admin}
		if err = t.RequireProjectAccess(issuer, stored.ProjectID, "owner"); err != nil {
			return Actor{}, denied()
		}
	default:
		return Actor{}, denied()
	}
	if stored.ID != a.ID || stored.Kind != a.Kind || stored.ProjectID != a.ProjectID || stored.WorkerID != a.WorkerID {
		return Actor{}, denied()
	}
	if c.Kind == "session" {
		return Actor{ID: identity.ID, Name: identity.Name, Kind: "human", Admin: identity.Admin, CredentialHash: a.CredentialHash}, nil
	}
	stored.Admin = false
	if stored.Kind == "worker" {
		worker, err := t.Get(stored.WorkerID)
		if err != nil || worker.Kind != "worker" || worker.Status == "disabled" || worker.ProjectID != stored.ProjectID || stored.ID != "worker:"+worker.ID {
			return Actor{}, denied()
		}
		stored.Roles = Strings(worker.Data["roles"])
	} else if stored.WorkerID != "" {
		return Actor{}, denied()
	}
	stored.CredentialHash = a.CredentialHash
	return stored, nil
}

func (t *Tx) RequireProjectAccess(a Actor, project string, roles ...string) error {
	current, err := t.CurrentActor(a)
	if err != nil {
		return err
	}
	a = current
	if project == "" || (a.ProjectID != "" && a.ProjectID != project) {
		return denied()
	}
	if a.Admin && a.Kind == "human" {
		return nil
	}
	actual := a.Roles
	if a.ProjectID == "" {
		var err error
		actual, err = t.ProjectRoles(a, project)
		if err != nil {
			return err
		}
	}
	if len(actual) == 0 {
		return denied()
	}
	if len(roles) == 0 || Has(actual, "owner") {
		return nil
	}
	for _, role := range roles {
		if Has(actual, role) {
			return nil
		}
	}
	return denied()
}

func (t *Tx) ProjectTokens(project string) ([]M, error) {
	rows, err := t.tx.Query(`SELECT hash,expires_at,body FROM credentials WHERE kind='token' ORDER BY expires_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var hash, expires, body string
		if err = rows.Scan(&hash, &expires, &body); err != nil {
			return nil, err
		}
		var actor Actor
		if err = json.Unmarshal([]byte(body), &actor); err != nil {
			return nil, err
		}
		if actor.ProjectID == project {
			out = append(out, M{"id": hash, "actor": actor, "expiresAt": expires})
		}
	}
	return out, rows.Err()
}
