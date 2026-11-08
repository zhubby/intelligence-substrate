package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

func (h *Handler) sourceSnapshot(w http.ResponseWriter, r *http.Request, a p.Actor, id string) {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/x-tar" {
		writeError(w, &p.Fault{Code: "media_type", Message: "源码快照必须使用 application/x-tar", Status: 415})
		return
	}
	headers := r.Header.Values("X-IS-Source")
	bad := func() {
		writeError(w, &p.Fault{Code: "invalid_metadata", Message: "需要唯一的 X-IS-Source 元数据，编码后不超过 8 KiB，且不允许查询参数", Status: 400})
	}
	if len(headers) != 1 || headers[0] == "" || len(headers[0]) > 8192 || r.URL.RawQuery != "" {
		bad()
		return
	}
	raw, e := base64.StdEncoding.Strict().DecodeString(headers[0])
	if e != nil {
		bad()
		return
	}
	var meta struct {
		Key              string `json:"key"`
		Branch           string `json:"branch"`
		BaseCommit       string `json:"baseCommit"`
		ExpectedHead     string `json:"expectedHead"`
		Message          string `json:"message"`
		RunID            string `json:"runId"`
		LeaseToken       string `json:"leaseToken"`
		IntegrationID    string `json:"integrationId"`
		ResolutionDigest string `json:"resolutionDigest"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(&meta); e != nil {
		bad()
		return
	}
	if e = d.Decode(new(any)); e != io.EOF {
		bad()
		return
	}
	in := p.M{"key": meta.Key, "branch": meta.Branch, "baseCommit": meta.BaseCommit, "expectedHead": meta.ExpectedHead, "message": meta.Message, "runId": meta.RunID, "leaseToken": meta.LeaseToken}
	if meta.IntegrationID != "" || meta.ResolutionDigest != "" {
		in["integrationId"], in["resolutionDigest"] = meta.IntegrationID, meta.ResolutionDigest
	}
	r.Body = http.MaxBytesReader(w, r.Body, source.MaxBytes)
	defer r.Body.Close()
	v, e := h.Service.CommitSnapshot(r.Context(), a, id, in, r.Body)
	reply(w, sanitize(v), e)
}
