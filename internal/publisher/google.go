package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"
)

func (e *Engine) googleTokenSource(ctx context.Context, p Profile) (oauth2.TokenSource, error) {
	body, err := readPrivateFile(p.CredentialsFile)
	if err != nil {
		return nil, err
	}
	var credentials struct {
		Type     string `json:"type"`
		Email    string `json:"client_email"`
		Key      string `json:"private_key"`
		KeyID    string `json:"private_key_id"`
		TokenURI string `json:"token_uri"`
	}
	if err = json.Unmarshal(body, &credentials); err != nil || credentials.Type != "service_account" || credentials.Email == "" || credentials.Key == "" || (credentials.TokenURI != "" && credentials.TokenURI != "https://oauth2.googleapis.com/token") {
		return nil, fmt.Errorf("invalid Google service-account credential file or token endpoint")
	}
	config := jwt.Config{Email: credentials.Email, PrivateKey: []byte(credentials.Key), PrivateKeyID: credentials.KeyID, Scopes: []string{"https://www.googleapis.com/auth/androidpublisher"}, TokenURL: "https://oauth2.googleapis.com/token"}
	return config.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, e.HTTP)), nil
}
func (e *Engine) googleClient(ctx context.Context, p Profile) (*http.Client, error) {
	source, err := e.tokenSource(ctx, p)
	if err != nil {
		return nil, err
	}
	// Authenticate before creating any external edit; do not include provider
	// response bodies or credential material in errors or execution logs.
	token, err := source.Token()
	if err != nil {
		return nil, fmt.Errorf("Google OAuth authentication failed")
	}
	return &http.Client{Transport: &oauth2.Transport{Source: oauth2.ReuseTokenSource(token, source), Base: e.HTTP.Transport}, Timeout: e.HTTP.Timeout, CheckRedirect: e.HTTP.CheckRedirect}, nil
}
func (e *Engine) googleCall(ctx context.Context, client *http.Client, method, path string, input any, out any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	request, err := http.NewRequestWithContext(ctx, method, e.apiBase+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	return googleResponse(client, request, out)
}
func googleResponse(client *http.Client, request *http.Request, out any) error {
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Google Play request interrupted; inspect persisted receipt before any resubmission")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Google Play API returned HTTP %d; inspect persisted receipt", response.StatusCode)
	}
	if out == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20+1))
	if err = decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid Google Play API response")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fmt.Errorf("trailing Google Play API response data")
	}
	return nil
}
func editPath(p Profile, editID string) string {
	return "/androidpublisher/v3/applications/" + url.PathEscape(p.AppID) + "/edits/" + url.PathEscape(editID)
}
func (e *Engine) googlePublish(ctx context.Context, p Profile, r Request, artifact string) (Result, error) {
	client, err := e.googleClient(ctx, p)
	if err != nil {
		return Result{}, err
	}
	var edit struct {
		ID string `json:"id"`
	}
	if err = e.googleCall(ctx, client, "POST", "/androidpublisher/v3/applications/"+url.PathEscape(p.AppID)+"/edits", struct{}{}, &edit); err != nil {
		return Result{}, err
	}
	if edit.ID == "" {
		return Result{}, fmt.Errorf("Google Play did not return an edit ID")
	}
	receipt := Receipt{EditID: edit.ID}
	if err = e.checkpointGoogle(ctx, p, r, receipt); err != nil {
		return Result{}, err
	}
	file, err := os.Open(artifact)
	if err != nil {
		return Result{}, err
	}
	defer file.Close()
	upload, err := http.NewRequestWithContext(ctx, "POST", e.apiBase+"/upload"+editPath(p, edit.ID)+"/bundles?uploadType=media", file)
	if err != nil {
		return Result{}, err
	}
	upload.Header.Set("Content-Type", "application/octet-stream")
	upload.ContentLength = r.ArtifactSize
	var bundle struct {
		VersionCode int64  `json:"versionCode"`
		SHA256      string `json:"sha256"`
	}
	if err = googleResponse(client, upload, &bundle); err != nil {
		return Result{}, err
	}
	if bundle.VersionCode <= 0 || strings.ToLower(bundle.SHA256) != r.ArtifactDigest {
		return Result{}, fmt.Errorf("Google Play upload receipt does not match approved artifact SHA256")
	}
	receipt.VersionCode = strconv.FormatInt(bundle.VersionCode, 10)
	if err = e.checkpointGoogle(ctx, p, r, receipt); err != nil {
		return Result{}, err
	}
	track := map[string]any{"track": p.Track, "releases": []any{map[string]any{"name": r.Version, "versionCodes": []string{receipt.VersionCode}, "status": "completed"}}}
	var trackResponse any
	if err = e.googleCall(ctx, client, "PUT", editPath(p, edit.ID)+"/tracks/"+url.PathEscape(p.Track), track, &trackResponse); err != nil {
		return Result{}, err
	}
	var committed struct {
		ID string `json:"id"`
	}
	if err = e.googleCall(ctx, client, "POST", editPath(p, edit.ID)+":commit?changesNotSentForReview=false", nil, &committed); err != nil {
		return Result{}, err
	}
	if committed.ID != edit.ID {
		return Result{}, fmt.Errorf("Google Play commit receipt is missing or inconsistent")
	}
	return Result{ExternalID: p.AppID + ":" + receipt.VersionCode, State: "awaiting_review", UpstreamState: "edit_committed", Reason: "提交已接受；商店审核与公开可用性仍需独立确认"}, nil
}
func (e *Engine) googleStatus(ctx context.Context, p Profile, r Request) (result Result, resultErr error) {
	parts := strings.Split(r.ExternalID, ":")
	if len(parts) != 2 || parts[0] != p.AppID {
		return Result{}, fmt.Errorf("Google Play version belongs to another application")
	}
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || version <= 0 {
		return Result{}, fmt.Errorf("invalid Google Play version code")
	}
	op, err := e.Inspect(ctx, r.OperationKey)
	if err != nil {
		return Result{}, err
	}
	if op.Receipt.VersionCode != parts[1] {
		return Result{}, fmt.Errorf("version code differs from persisted upload receipt")
	}
	session, err := e.openGoogleQuery(ctx, p, r, "status", op.Receipt.EditID)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if err := session.close(ctx); err != nil {
			result = Result{}
			if resultErr == nil {
				resultErr = err
			} else {
				resultErr = fmt.Errorf("%w; query cleanup unconfirmed, retain its durable window", resultErr)
			}
		}
	}()
	var track struct {
		Track    string `json:"track"`
		Releases []struct {
			VersionCodes []string `json:"versionCodes"`
			Status       string   `json:"status"`
		} `json:"releases"`
	}
	if err = e.googleCall(ctx, session.client, "GET", editPath(p, session.editID)+"/tracks/"+url.PathEscape(p.Track), nil, &track); err != nil {
		return Result{}, err
	}
	if track.Track != p.Track {
		return Result{}, fmt.Errorf("Google Play returned a different track")
	}
	for _, release := range track.Releases {
		for _, code := range release.VersionCodes {
			if code == parts[1] {
				state := "unchanged"
				if release.Status == "halted" {
					state = "halted"
				}
				return Result{ExternalID: r.ExternalID, State: state, UpstreamState: release.Status, Reason: "Track 状态不证明商店审核通过或已公开上架；请附实际渠道结果后确认"}, nil
			}
		}
	}
	return Result{}, fmt.Errorf("expected Google Play version absent from track; retain known state and reconcile")
}
