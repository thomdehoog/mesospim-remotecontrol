// Package httpapi exposes the Foundation as a REST API.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/thomdehoog/origoa/internal/core"
	"github.com/thomdehoog/origoa/internal/ojson"
)

const (
	maxBody      = 4 << 20 // 4 MiB request bodies
	defaultLimit = 1000
	maxLimit     = 10000
)

type Server struct {
	f *core.Foundation
}

func New(f *core.Foundation) http.Handler {
	s := &Server{f: f}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/tree", s.tree)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("POST /api/admin/reindex", s.reindex)

	// Artifact CRUD; the route's collection is part of the contract, so a
	// GUID of another kind is 404 on that route.
	for route, kind := range map[string]string{
		"entries": core.KindEntry, "documents": core.KindDocument,
		"links": core.KindLink, "comments": core.KindComment,
	} {
		kind := kind
		mux.HandleFunc("GET /api/"+route+"/{guid}", func(w http.ResponseWriter, r *http.Request) { s.get(w, r, kind) })
		mux.HandleFunc("DELETE /api/"+route+"/{guid}", func(w http.ResponseWriter, r *http.Request) { s.delete(w, r, kind) })
	}
	for route, kind := range map[string]string{"entries": core.KindEntry, "documents": core.KindDocument} {
		kind := kind
		mux.HandleFunc("POST /api/"+route, func(w http.ResponseWriter, r *http.Request) { s.create(w, r, kind) })
		mux.HandleFunc("PUT /api/"+route+"/{guid}", func(w http.ResponseWriter, r *http.Request) { s.update(w, r, kind) })
	}

	mux.HandleFunc("POST /api/links", s.createLink)
	mux.HandleFunc("POST /api/comments", s.createComment)

	mux.HandleFunc("GET /api/artifacts/{guid}/links", s.artifactLinks)
	mux.HandleFunc("GET /api/artifacts/{guid}/comments", s.artifactComments)
	mux.HandleFunc("GET /api/artifacts/{guid}/history", s.history)
	mux.HandleFunc("POST /api/artifacts/{guid}/move", s.move)
	mux.HandleFunc("POST /api/artifacts/{guid}/transition", s.transition)

	mux.HandleFunc("GET /api/schemas", s.schemas)
	mux.HandleFunc("GET /api/schemas/effective", s.effectiveSchema)
	mux.HandleFunc("PUT /api/schemas/{name}", s.putSchema)
	mux.HandleFunc("GET /api/workflows/{id}", s.workflow)
	mux.HandleFunc("PUT /api/workflows/{name}", s.putWorkflow)

	return mux
}

// ---- plumbing ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr maps domain errors to status codes. Internal failures are logged
// server-side and returned as a generic message — git/db details (paths,
// revisions, stderr) never leak to clients.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, core.ErrPrecondition):
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": err.Error()})
	case errors.Is(err, core.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, core.ErrValidation):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, core.ErrUnavailable):
		log.Printf("httpapi: projection unavailable: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "projection temporarily unavailable"})
	default:
		log.Printf("httpapi: internal error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

func readBody(w http.ResponseWriter, req *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxBody))
	if err == nil {
		err = json.Unmarshal(body, v)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return false
	}
	return true
}

func limitParam(req *http.Request) int {
	n, err := strconv.Atoi(req.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return defaultLimit
	}
	return min(n, maxLimit)
}

// setETag / ifMatch implement RFC 9110 entity-tag syntax over the blob SHA.
func setETag(w http.ResponseWriter, etag string) {
	w.Header().Set("ETag", `"`+etag+`"`)
}

func ifMatch(req *http.Request) string {
	v := strings.TrimSpace(req.Header.Get("If-Match"))
	if v == "" || v == "*" { // "*" = any current representation
		return ""
	}
	v = strings.TrimPrefix(v, "W/")
	return strings.Trim(v, `"`)
}

// metaOfKind resolves a GUID and enforces the route's artifact kind.
func (s *Server) metaOfKind(guid, kind string) (*core.Meta, error) {
	m, err := s.f.Meta(guid)
	if err != nil {
		return nil, err
	}
	if m.Kind != kind {
		return nil, core.ErrNotFound
	}
	return m, nil
}

// ---- artifacts ----

func (s *Server) tree(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	subtree := q.Get("subtree") != "0"
	artifacts, err := s.f.List(q.Get("kind"), q.Get("type"), q.Get("path"), subtree, limitParam(req))
	if err != nil {
		writeErr(w, err)
		return
	}
	folders, err := s.f.Folders()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rev": s.f.Head(), "folders": folders, "artifacts": artifacts,
	})
}

func (s *Server) search(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	artifacts, err := s.f.Search(q.Get("q"), q.Get("kind"), q.Get("type"), limitParam(req))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": artifacts})
}

func (s *Server) reindex(w http.ResponseWriter, _ *http.Request) {
	if err := s.f.Reindex(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"rev": s.f.Head()})
}

func (s *Server) create(w http.ResponseWriter, req *http.Request, kind string) {
	body := ojson.New()
	if !readBody(w, req, body) {
		return
	}
	meta, err := s.f.CreateArtifact(kind, body.GetString("path"), body.GetString("type"), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	setETag(w, meta.ETag)
	writeJSON(w, http.StatusCreated, map[string]any{"meta": meta})
}

func (s *Server) get(w http.ResponseWriter, req *http.Request, kind string) {
	if _, err := s.metaOfKind(req.PathValue("guid"), kind); err != nil {
		writeErr(w, err)
		return
	}
	meta, obj, err := s.f.Artifact(req.PathValue("guid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	res := map[string]any{"meta": meta, "data": obj}
	if req.URL.Query().Get("resolve") == "1" && meta.Kind == core.KindEntry {
		fields, chain, err := s.f.ResolveOverlay(meta.GUID)
		if err != nil {
			writeErr(w, err)
			return
		}
		res["resolved"] = map[string]any{"fields": fields, "chain": chain}
	}
	setETag(w, meta.ETag)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) update(w http.ResponseWriter, req *http.Request, kind string) {
	if _, err := s.metaOfKind(req.PathValue("guid"), kind); err != nil {
		writeErr(w, err)
		return
	}
	patch := ojson.New()
	if !readBody(w, req, patch) {
		return
	}
	meta, err := s.f.UpdateArtifact(req.PathValue("guid"), patch, ifMatch(req))
	if err != nil {
		writeErr(w, err)
		return
	}
	setETag(w, meta.ETag)
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta})
}

func (s *Server) delete(w http.ResponseWriter, req *http.Request, kind string) {
	if _, err := s.metaOfKind(req.PathValue("guid"), kind); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.f.DeleteArtifact(req.PathValue("guid")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) move(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readBody(w, req, &body) {
		return
	}
	meta, err := s.f.MoveArtifact(req.PathValue("guid"), body.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta})
}

// ---- links & comments ----

func (s *Server) createLink(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Type   string          `json:"type"`
		Source string          `json:"source"`
		Target string          `json:"target"`
		Fields json.RawMessage `json:"fields"`
	}
	if !readBody(w, req, &body) {
		return
	}
	meta, err := s.f.CreateLink(body.Type, body.Source, body.Target, body.Fields)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"meta": meta})
}

func (s *Server) createComment(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Subject string `json:"subject"`
		Text    string `json:"text"`
		Parent  string `json:"parent"`
		Author  string `json:"author"`
	}
	if !readBody(w, req, &body) {
		return
	}
	meta, err := s.f.CreateComment(body.Subject, body.Text, body.Parent, body.Author)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"meta": meta})
}

func (s *Server) artifactLinks(w http.ResponseWriter, req *http.Request) {
	guid := req.PathValue("guid")
	if _, err := s.f.Meta(guid); err != nil {
		writeErr(w, err)
		return
	}
	in, out, err := s.f.Links(guid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incoming": in, "outgoing": out})
}

func (s *Server) artifactComments(w http.ResponseWriter, req *http.Request) {
	guid := req.PathValue("guid")
	if _, err := s.f.Meta(guid); err != nil {
		writeErr(w, err)
		return
	}
	metas, err := s.f.Comments(guid)
	if err != nil {
		writeErr(w, err)
		return
	}
	objs, err := s.f.Objects(metas) // one batch read, ordered like metas
	if err != nil {
		writeErr(w, err)
		return
	}
	comments := make([]map[string]any, 0, len(objs))
	for i, obj := range objs {
		comments = append(comments, map[string]any{"meta": metas[i], "data": obj})
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": comments})
}

func (s *Server) history(w http.ResponseWriter, req *http.Request) {
	limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	log, err := s.f.History(req.PathValue("guid"), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": log})
}

func (s *Server) transition(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Workflow string `json:"workflow"`
		To       string `json:"to"`
	}
	if !readBody(w, req, &body) {
		return
	}
	meta, err := s.f.Transition(req.PathValue("guid"), body.Workflow, body.To)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta})
}

// ---- configuration ----

func (s *Server) schemas(w http.ResponseWriter, _ *http.Request) {
	schemas, err := s.f.Schemas()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schemas": schemas})
}

func (s *Server) effectiveSchema(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	schema, err := s.f.EffectiveSchema(q.Get("type"), q.Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": schema})
}

func (s *Server) putSchema(w http.ResponseWriter, req *http.Request) {
	var schema core.Schema
	if !readBody(w, req, &schema) {
		return
	}
	if err := s.f.PutSchema(req.URL.Query().Get("scope"), req.PathValue("name"), &schema); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": schema})
}

func (s *Server) workflow(w http.ResponseWriter, req *http.Request) {
	wf, err := s.f.WorkflowDef(req.PathValue("id"), req.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow": wf})
}

func (s *Server) putWorkflow(w http.ResponseWriter, req *http.Request) {
	var wf core.Workflow
	if !readBody(w, req, &wf) {
		return
	}
	if err := s.f.PutWorkflow(req.URL.Query().Get("scope"), req.PathValue("name"), &wf); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow": wf})
}
