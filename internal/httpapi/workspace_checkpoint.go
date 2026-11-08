package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

func checkpointMetadata(raw []byte) (p.M, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return nil, io.ErrUnexpectedEOF
	}
	out := p.M{}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return nil, e
		}
		name, ok := key.(string)
		if !ok || !p.Has([]string{"leaseToken", "repositoryId", "commit"}, name) {
			return nil, io.ErrUnexpectedEOF
		}
		if _, exists := out[name]; exists {
			return nil, io.ErrUnexpectedEOF
		}
		var value any
		if e = d.Decode(&value); e != nil {
			return nil, e
		}
		text, ok := value.(string)
		if !ok {
			return nil, io.ErrUnexpectedEOF
		}
		out[name] = text
	}
	if _, e = d.Token(); e != nil {
		return nil, e
	}
	if len(out) != 3 {
		return nil, io.ErrUnexpectedEOF
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return nil, io.ErrUnexpectedEOF
	}
	return out, nil
}

func (h *Handler) workspaceCheckpoint(w http.ResponseWriter, r *http.Request, a p.Actor, id string) {
	bad := func() {
		writeError(w, &p.Fault{Code: "invalid_metadata", Message: "工作区需要唯一且有界的元数据，不允许查询参数", Status: 400})
	}
	if r.URL.RawQuery != "" {
		bad()
		return
	}
	if r.Method == "GET" {
		leases := r.Header.Values("X-IS-Lease")
		if len(leases) != 1 || leases[0] == "" || len(leases[0]) > 1024 {
			bad()
			return
		}
		file, checkpoint, e := h.Service.WorkspaceCheckpoint(r.Context(), a, id, leases[0])
		if e != nil {
			writeError(w, e)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", `attachment; filename="workspace.tar"`)
		w.Header().Set("Content-Length", strconv.FormatInt(int64(p.N(checkpoint.Data, "size")), 10))
		_, _ = io.Copy(w, file)
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/x-tar" {
		writeError(w, &p.Fault{Code: "media_type", Message: "工作区归档必须使用 application/x-tar", Status: 415})
		return
	}
	headers := r.Header.Values("X-IS-Checkpoint")
	if len(headers) != 1 || headers[0] == "" || len(headers[0]) > 8192 {
		bad()
		return
	}
	raw, e := base64.StdEncoding.Strict().DecodeString(headers[0])
	if e != nil {
		bad()
		return
	}
	in, e := checkpointMetadata(raw)
	if e != nil {
		bad()
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, source.MaxBytes)
	defer r.Body.Close()
	checkpoint, e := h.Service.UploadWorkspaceCheckpoint(r.Context(), a, id, in, r.Body)
	reply(w, sanitize(checkpoint), e)
}
