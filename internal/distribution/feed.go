package distribution

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const feedDomain = "intelligence-substrate-update-v1\n"

type Feed struct {
	Release
	SchemaVersion int    `json:"schemaVersion"`
	URL           string `json:"url"`
	IssuedAt      string `json:"issuedAt"`
	ExpiresAt     string `json:"expiresAt"`
}
type SignedFeed struct {
	KeyID     string `json:"keyId"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

func KeyID(key ed25519.PublicKey) string {
	digest := sha256.Sum256(key)
	return hex.EncodeToString(digest[:])
}

func signFeed(r Release, sequence int64, publicURL string, key ed25519.PrivateKey, issued time.Time) (SignedFeed, error) {
	r.Sequence = sequence
	// Internal idempotency hashes stay private; provenance is public.
	r.RequestHash = ""
	f := Feed{Release: r, SchemaVersion: 1, URL: strings.TrimRight(publicURL, "/") + "/downloads/" + r.App + "/" + r.Target + "/" + r.ID + "/" + r.Filename, IssuedAt: issued.UTC().Format(time.RFC3339), ExpiresAt: issued.Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)}
	body, err := json.Marshal(f)
	if err != nil {
		return SignedFeed{}, err
	}
	return SignedFeed{KeyID: KeyID(key.Public().(ed25519.PublicKey)), Payload: base64.StdEncoding.EncodeToString(body), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(feedDomain), body...)))}, nil
}

// VerifyFeed requires a separately pinned public key and remembered minimum
// sequence. Trusting the key from the downloaded envelope defeats verification.
func VerifyFeed(s SignedFeed, key ed25519.PublicKey, app, target, channel string, minimumSequence int64, at time.Time) (Feed, error) {
	var f Feed
	if len(key) != ed25519.PublicKeySize || s.KeyID != KeyID(key) {
		return f, fmt.Errorf("untrusted update signing key")
	}
	body, err := base64.StdEncoding.DecodeString(s.Payload)
	if err != nil || len(body) > 32000 {
		return f, fmt.Errorf("invalid update payload")
	}
	signature, err := base64.StdEncoding.DecodeString(s.Signature)
	if err != nil || !ed25519.Verify(key, append([]byte(feedDomain), body...), signature) {
		return f, fmt.Errorf("invalid update signature")
	}
	if err = json.Unmarshal(body, &f); err != nil {
		return f, err
	}
	issued, e1 := time.Parse(time.RFC3339, f.IssuedAt)
	expires, e2 := time.Parse(time.RFC3339, f.ExpiresAt)
	if f.SchemaVersion != 1 || f.App != app || f.Target != target || f.Channel != channel || f.Sequence < 1 || f.Sequence < minimumSequence || !sha256Pattern.MatchString(f.Digest) || f.Size < 1 || e1 != nil || e2 != nil || issued.After(at.Add(5*time.Minute)) || !expires.After(at) || expires.After(issued.Add(7*24*time.Hour)) {
		return f, fmt.Errorf("stale or mismatched update index")
	}
	return f, nil
}
