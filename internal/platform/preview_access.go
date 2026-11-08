package platform

import (
	"context"
	"encoding/json"
	"time"
)

type PreviewAccess struct {
	Token     string `json:"-"`
	ExpiresAt string `json:"expiresAt"`
}

type previewGrant struct {
	ProjectID   string `json:"projectId"`
	PreviewID   string `json:"previewId"`
	ActorID     string `json:"actorId"`
	SessionHash string `json:"sessionHash"`
}

// IssuePreviewAccess never persists or audits the plaintext access token. A
// preview credential cannot be used by CurrentActor or ordinary HTTP auth.
func (s *Service) IssuePreviewAccess(ctx context.Context, a Actor, id string) (PreviewAccess, error) {
	result := PreviewAccess{Token: Secret()}
	err := s.Store.Update(ctx, func(t *Tx) error {
		current, err := t.CurrentActor(a)
		if err != nil {
			return err
		}
		a = current
		if a.Kind != "human" || a.CredentialHash == "" {
			return denied()
		}
		parent, err := t.Credential(a.CredentialHash)
		if err != nil || parent.Kind != "session" || parent.IdentityID != a.ID {
			return denied()
		}
		r, _, err := s.currentPreview(t, id)
		if err != nil {
			return err
		}
		if err = t.RequireProjectAccess(a, r.ProjectID); err != nil {
			return err
		}
		// Use the same serialized transaction as offboarding. Expired access
		// rows are disposable credentials, not business or audit history.
		if _, err = t.tx.Exec(`DELETE FROM credentials WHERE identity_id=$1 AND kind='preview' AND expires_at<$2`, a.ID, now()); err != nil {
			return err
		}
		var count int
		if err = t.tx.QueryRow(`SELECT count(*) FROM credentials WHERE identity_id=$1 AND kind='preview'`, a.ID).Scan(&count); err != nil {
			return err
		}
		if count >= 20 {
			return conflict("短期预览链接达到 20 个，请等待旧链接到期后重试")
		}
		deadline := time.Now().Add(10 * time.Minute)
		for _, value := range []string{parent.ExpiresAt, S(r.Data, "expiresAt")} {
			end, err := time.Parse(time.RFC3339Nano, value)
			if err != nil || !end.After(time.Now()) {
				return denied()
			}
			if end.Before(deadline) {
				deadline = end
			}
		}
		result.ExpiresAt = deadline.UTC().Format(time.RFC3339Nano)
		body, err := json.Marshal(previewGrant{ProjectID: r.ProjectID, PreviewID: r.ID, ActorID: a.ID, SessionHash: a.CredentialHash})
		if err != nil {
			return err
		}
		if err = t.InsertCredential(Hash(result.Token), Credential{IdentityID: a.ID, Kind: "preview", ExpiresAt: result.ExpiresAt, Body: string(body)}); err != nil {
			return err
		}
		return t.Audit(a, "previews.access.issue", r, M{"expiresAt": result.ExpiresAt})
	})
	if err != nil {
		return PreviewAccess{}, err
	}
	return result, nil
}

// ResolvePreviewAccess checks both session revocation and current project
// authority on every request, including cache hits and asset subrequests.
func (s *Service) ResolvePreviewAccess(ctx context.Context, token string) (preview, artifact Resource, err error) {
	err = s.Store.Update(ctx, func(t *Tx) error {
		c, e := t.Credential(Hash(token))
		if e != nil || c.Kind != "preview" {
			return missing()
		}
		deadline, e := time.Parse(time.RFC3339Nano, c.ExpiresAt)
		if e != nil || !deadline.After(time.Now()) {
			return missing()
		}
		var grant previewGrant
		if e = json.Unmarshal([]byte(c.Body), &grant); e != nil || grant.ActorID == "" || grant.ActorID != c.IdentityID || grant.SessionHash == "" {
			return missing()
		}
		a, e := t.CurrentActor(Actor{ID: grant.ActorID, Kind: "human", CredentialHash: grant.SessionHash})
		if e != nil {
			return missing()
		}
		if e = t.RequireProjectAccess(a, grant.ProjectID); e != nil {
			return missing()
		}
		preview, artifact, e = s.currentPreview(t, grant.PreviewID)
		if e != nil || preview.ProjectID != grant.ProjectID {
			return missing()
		}
		return nil
	})
	return
}
