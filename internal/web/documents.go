package web

import (
	"bytes"
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"
)

var ErrInvalidDocument = errors.New("not a PDF document")

func servePDF(w http.ResponseWriter, r *http.Request, data []byte, digest, name, cache string) {
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(name)}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", cache)
	if digest != "" {
		w.Header().Set("ETag", `"`+digest+`"`)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func (s *Server) document(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	generation, err := strconv.ParseUint(q.Get("claim_generation"), 10, 32)
	if err != nil {
		badRequest(w, err)
		return
	}
	value, err := s.backend.WorkspaceDocument(r.Context(), id, WorkspaceFileRequest{q.Get("path"), q.Get("claim_id"), uint32(generation)})
	if err != nil {
		reply(w, struct{}{}, err)
		return
	}
	// The core permits only bounded PDF snapshots. Never infer executable MIME
	// types from a filename or serve an HTML/SVG document as this origin.
	if value.MediaType != "application/pdf" || !bytes.HasPrefix(value.Content, []byte("%PDF-")) {
		badRequest(w, ErrInvalidDocument)
		return
	}
	servePDF(w, r, value.Content, value.Sha256, q.Get("path"), "no-store")
}
