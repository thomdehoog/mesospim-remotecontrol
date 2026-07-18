// Package api exposes the Origoa Foundation over HTTP.
//
// Two complementary endpoint categories are provided: artifact APIs
// (generic CRUD for the native artifacts) and service APIs (search,
// tree browsing, effective schema resolution, overlay analysis, workflow
// evaluation, relationship analysis, history and maintenance). A
// WebSocket session service distributes transient runtime information —
// presence, repository events, maintenance and indexing progress.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"origoa/internal/projection"
	"origoa/internal/repo"
)

// Server wires the repository service into HTTP handlers.
type Server struct {
	Svc *repo.Service
	Hub *Hub

	// StaticDir optionally serves the frontend build (SPA fallback).
	StaticDir string

	// CORSOrigin enables cross-origin requests from the given origin ("*"
	// for any). Empty disables CORS entirely, which is correct for the
	// production topology where the API serves the SPA same-origin.
	CORSOrigin string
}

// NewServer creates the API server and connects the event hub.
func NewServer(svc *repo.Service, staticDir, corsOrigin string) *Server {
	hub := NewHub(corsOrigin)
	svc.EventSink = func(e repo.Event) { hub.BroadcastEvent(e) }
	return &Server{Svc: svc, Hub: hub, StaticDir: staticDir, CORSOrigin: corsOrigin}
}

// Handler builds the route table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Service APIs
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/reindex", s.handleReindex)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/schemas", s.handleSchemas)
	mux.HandleFunc("GET /api/schemas/effective", s.handleEffectiveSchema)
	mux.HandleFunc("GET /api/workflows/{name}", s.handleWorkflowDef)

	// Artifact APIs
	mux.HandleFunc("POST /api/entries", s.handleCreateEntry)
	mux.HandleFunc("POST /api/documents", s.handleCreateDocument)
	mux.HandleFunc("GET /api/artifacts/{guid}", s.handleGetArtifact)
	mux.HandleFunc("PATCH /api/artifacts/{guid}", s.handleUpdateArtifact)
	mux.HandleFunc("DELETE /api/artifacts/{guid}", s.handleDeleteArtifact)
	mux.HandleFunc("POST /api/artifacts/{guid}/move", s.handleMoveArtifact)
	mux.HandleFunc("GET /api/artifacts/{guid}/links", s.handleArtifactLinks)
	mux.HandleFunc("GET /api/artifacts/{guid}/comments", s.handleArtifactComments)
	mux.HandleFunc("GET /api/artifacts/{guid}/history", s.handleArtifactHistory)
	mux.HandleFunc("GET /api/artifacts/{guid}/overlay", s.handleOverlay)
	mux.HandleFunc("POST /api/artifacts/{guid}/workflows/{name}/transition", s.handleTransition)
	mux.HandleFunc("POST /api/links", s.handleCreateLink)
	mux.HandleFunc("POST /api/comments", s.handleCreateComment)

	// Folder operations
	mux.HandleFunc("POST /api/folders", s.handleCreateFolder)
	mux.HandleFunc("POST /api/folders/move", s.handleMoveFolder)
	mux.HandleFunc("DELETE /api/folders", s.handleDeleteFolder)
	mux.HandleFunc("GET /api/folders/impact", s.handleFolderImpact)

	// Session service
	mux.HandleFunc("GET /api/ws", s.Hub.HandleWS)

	if s.StaticDir != "" {
		mux.HandleFunc("/", s.handleStatic)
	}
	var h http.Handler = mux
	if s.CORSOrigin != "" {
		h = withCORS(h, s.CORSOrigin)
	}
	return recoverPanic(securityHeaders(h))
}

// securityHeaders applies conservative response hardening. The
// Content-Security-Policy is defense-in-depth behind the HTML sanitizer:
// scripts and framing are restricted to same-origin, styles allow the inline
// stylesheet the app ships, and the API/WebSocket connection is same-origin.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https:; " +
		"connect-src 'self' ws: wss:; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// recoverPanic turns a panic in any handler into a clean 500 and a log line,
// so a single bad request can never take the server down.
func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("api: panic serving %s %s: %v", r.Method, r.URL.Path, v)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"internal error"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withCORS adds permissive CORS headers for the configured origin. Used for
// the split-origin development topology (Vite dev server on a different
// port); production serves the SPA same-origin and needs no CORS.
func withCORS(next http.Handler, origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleStatic serves the SPA build with an index.html fallback so deep
// links reconstruct the application state client-side.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	p := filepath.Join(s.StaticDir, filepath.Clean("/"+r.URL.Path))
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		http.ServeFile(w, r, p)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.StaticDir, "index.html"))
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}

type apiError struct {
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, err error) {
	var ve repo.ErrValidation
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, apiError{ve.Msg})
	case errors.Is(err, repo.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{"not found"})
	case errors.Is(err, repo.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{"the artifact was modified concurrently; reload and retry"})
	case errors.Is(err, repo.ErrMaintenance):
		writeJSON(w, http.StatusServiceUnavailable, apiError{"repository is temporarily in maintenance mode"})
	default:
		log.Printf("api: internal error: %v", err)
		writeJSON(w, http.StatusInternalServerError, apiError{"internal error"})
	}
}

func readBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&v); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{"invalid JSON body: " + err.Error()})
		return v, false
	}
	return v, true
}

// rawContent re-emits stored artifact JSON without re-encoding noise.
type rawContent []byte

func (r rawContent) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

func artifactJSON(a *projection.ArtifactRow) map[string]any {
	return map[string]any{
		"guid":          a.GUID,
		"kind":          a.Kind,
		"type":          a.Type,
		"title":         a.Title,
		"hid":           a.HID,
		"repoPath":      a.RepoPath,
		"parentPath":    a.ParentPath,
		"updatedCommit": a.UpdatedCommit,
		"linkCount":     a.LinkCount,
		"commentCount":  a.CommentCount,
		"content":       rawContent(a.Content),
	}
}

func artifactListJSON(rows []projection.ArtifactRow) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i := range rows {
		out[i] = artifactJSON(&rows[i])
	}
	return out
}

// Serve starts the HTTP server and shuts it down gracefully when ctx is
// cancelled.
//
// ReadHeaderTimeout and IdleTimeout bound slow/idle connections (Slowloris)
// without a global read/write timeout, which would sever the long-lived
// WebSocket session connections. Request bodies are separately capped by
// MaxBytesReader at the handler.
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()
	log.Printf("origoa: listening on %s", addr)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func queryBool(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true" || v == "yes"
}
