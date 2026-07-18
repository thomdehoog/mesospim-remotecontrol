package api

import (
	"net/http"
	"strconv"
	"strings"

	"origoa/internal/artifact"
	"origoa/internal/projection"
	"origoa/internal/repo"
	"origoa/internal/resolve"
)

// ---- Service APIs ----

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Svc.DB.GetStats(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	head, err := s.Svc.Git.Head()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"revision":    stats.Revision,
		"gitHead":     head.String(),
		"maintenance": s.Svc.Maintenance(),
		"reindex":     s.Svc.Progress(),
		"stats":       stats,
	})
}

func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	// The reindex outlives this request, so it must not run on the request
	// context (cancelled the moment the handler returns). It is tied to the
	// server lifetime instead, so it still stops on graceful shutdown.
	go func() {
		if err := s.Svc.Reindex(s.baseContext()); err != nil {
			s.Hub.BroadcastEvent(repo.Event{Type: "reindex-failed", Detail: err.Error()})
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// handleTree returns the navigation payload for one folder: sub-folders,
// contained artifacts and (subtree) type groupings.
func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	folder := strings.Trim(r.URL.Query().Get("path"), "/")
	subtree := queryBool(r, "subtree")

	folders, err := s.Svc.DB.ChildFolders(ctx, folder)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := projection.SearchQuery{Folder: folder, Subtree: subtree, Limit: 500}
	rows, err := s.Svc.DB.Search(ctx, q)
	if err != nil {
		writeErr(w, err)
		return
	}
	var visible []projection.ArtifactRow
	for _, a := range rows {
		if a.Kind == artifact.KindEntry || a.Kind == artifact.KindDocument {
			visible = append(visible, a)
		}
	}
	types, err := s.Svc.DB.TypeCounts(ctx, folder)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      folder,
		"folders":   folders,
		"artifacts": artifactListJSON(visible),
		"types":     types,
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if s.Svc.Progress().Running {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"search is temporarily unavailable during reindexing"})
		return
	}
	q := projection.SearchQuery{
		Text:    r.URL.Query().Get("q"),
		Kind:    r.URL.Query().Get("kind"),
		Type:    r.URL.Query().Get("type"),
		Folder:  strings.Trim(r.URL.Query().Get("path"), "/"),
		Subtree: queryBool(r, "subtree"),
		Fields:  map[string]string{},
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		q.Limit = n
	}
	for key, vals := range r.URL.Query() {
		if strings.HasPrefix(key, "field.") && len(vals) > 0 {
			q.Fields[strings.TrimPrefix(key, "field.")] = vals[0]
		}
	}
	rows, err := s.Svc.DB.Search(r.Context(), q)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": artifactListJSON(rows)})
}

func (s *Server) handleSchemas(w http.ResponseWriter, r *http.Request) {
	folder := strings.Trim(r.URL.Query().Get("path"), "/")
	types, err := resolve.AvailableTypes(r.Context(), s.Svc.DB, nil, folder)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": types})
}

func (s *Server) handleEffectiveSchema(w http.ResponseWriter, r *http.Request) {
	folder := strings.Trim(r.URL.Query().Get("path"), "/")
	typ := r.URL.Query().Get("type")
	if typ == "" {
		writeJSON(w, http.StatusBadRequest, apiError{"type parameter is required"})
		return
	}
	eff, err := resolve.EffectiveSchema(r.Context(), s.Svc.DB, nil, folder, typ)
	if err != nil {
		writeErr(w, err)
		return
	}
	if eff == nil {
		writeJSON(w, http.StatusNotFound, apiError{"no schema defines type " + typ + " at this location"})
		return
	}
	writeJSON(w, http.StatusOK, eff)
}

func (s *Server) handleWorkflowDef(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	folder := strings.Trim(r.URL.Query().Get("path"), "/")
	wd, err := resolve.Workflow(r.Context(), s.Svc.DB, nil, folder, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, wd)
}

// ---- Artifact APIs ----

func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[repo.CreateArtifactParams](w, r)
	if !ok {
		return
	}
	p.Kind = artifact.KindEntry
	s.createArtifact(w, r, p)
}

func (s *Server) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[repo.CreateArtifactParams](w, r)
	if !ok {
		return
	}
	p.Kind = artifact.KindDocument
	s.createArtifact(w, r, p)
}

func (s *Server) createArtifact(w http.ResponseWriter, r *http.Request, p repo.CreateArtifactParams) {
	guid, err := s.Svc.CreateArtifact(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	row, err := s.Svc.DB.GetArtifact(r.Context(), guid)
	if err != nil || row == nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, artifactJSON(row))
}

// handleGetArtifact returns the artifact together with the repository
// intelligence the UI needs: effective schema, overlay resolution,
// workflow definitions with available transitions, relationships,
// comments and identifier history.
func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	guid := r.PathValue("guid")
	row, err := s.Svc.DB.GetArtifact(ctx, guid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if row == nil {
		// Deleted artifacts remain inspectable through their tombstone.
		if d, derr := s.Svc.DB.GetDeletedArtifact(ctx, guid); derr == nil && d != nil {
			writeJSON(w, http.StatusGone, map[string]any{"deleted": d})
			return
		}
		writeErr(w, repo.ErrNotFound)
		return
	}
	resp := artifactJSON(row)

	af, err := artifact.Parse(row.Content)
	if err == nil && row.Type != "" {
		if eff, err := resolve.EffectiveSchema(ctx, s.Svc.DB, nil, row.ParentPath, row.Type); err == nil && eff != nil {
			resp["schema"] = eff
			// Workflow definitions and available transitions.
			var wfs []map[string]any
			for _, wfName := range eff.Workflows {
				wd, err := resolve.Workflow(ctx, s.Svc.DB, nil, row.ParentPath, wfName)
				if err != nil {
					continue
				}
				state := af.Workflows[wfName]
				if state == "" {
					state = wd.Initial
				}
				wfs = append(wfs, map[string]any{
					"definition":  wd,
					"state":       state,
					"transitions": wd.TransitionsFrom(state),
				})
			}
			resp["workflows"] = wfs
		}
	}
	if row.Kind == artifact.KindEntry {
		if resolved, err := s.Svc.ResolveOverlay(ctx, guid); err == nil {
			resp["resolved"] = resolved
		}
	}
	if links, err := s.Svc.DB.LinksFor(ctx, guid); err == nil {
		resp["links"] = linksJSON(links)
	}
	if comments, err := s.Svc.DB.CommentsFor(ctx, guid); err == nil {
		resp["comments"] = commentsJSON(comments)
	}
	if hist, err := s.Svc.DB.HIDHistory(ctx, guid); err == nil {
		resp["hidHistory"] = hist
	}
	writeJSON(w, http.StatusOK, resp)
}

func linksJSON(links []projection.LinkRow) []map[string]any {
	out := make([]map[string]any, len(links))
	for i, l := range links {
		out[i] = map[string]any{
			"guid": l.GUID, "type": l.Type,
			"source": l.Source, "target": l.Target,
			"sourceTitle": l.SourceTitle, "targetTitle": l.TargetTitle,
			"content": rawContent(l.Content),
		}
	}
	return out
}

func commentsJSON(comments []projection.CommentRow) []map[string]any {
	out := make([]map[string]any, len(comments))
	for i, c := range comments {
		out[i] = map[string]any{
			"guid": c.GUID, "subject": c.Subject, "parent": c.Parent,
			"content": rawContent(c.Content),
		}
	}
	return out
}

func (s *Server) handleUpdateArtifact(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[repo.UpdateArtifactParams](w, r)
	if !ok {
		return
	}
	guid := r.PathValue("guid")
	if err := s.Svc.UpdateArtifact(r.Context(), guid, p); err != nil {
		writeErr(w, err)
		return
	}
	row, err := s.Svc.DB.GetArtifact(r.Context(), guid)
	if err != nil || row == nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactJSON(row))
}

func (s *Server) handleDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	guid := r.PathValue("guid")
	if err := s.Svc.DeleteArtifact(r.Context(), guid, r.URL.Query().Get("ifRevision")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": guid})
}

func (s *Server) handleMoveArtifact(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[struct {
		Folder     string `json:"folder"`
		IfRevision string `json:"ifRevision"`
	}](w, r)
	if !ok {
		return
	}
	guid := r.PathValue("guid")
	if err := s.Svc.MoveArtifact(r.Context(), guid, p.Folder, p.IfRevision); err != nil {
		writeErr(w, err)
		return
	}
	row, err := s.Svc.DB.GetArtifact(r.Context(), guid)
	if err != nil || row == nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactJSON(row))
}

func (s *Server) handleArtifactLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.Svc.DB.LinksFor(r.Context(), r.PathValue("guid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": linksJSON(links)})
}

func (s *Server) handleArtifactComments(w http.ResponseWriter, r *http.Request) {
	comments, err := s.Svc.DB.CommentsFor(r.Context(), r.PathValue("guid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": commentsJSON(comments)})
}

func (s *Server) handleArtifactHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	guid := r.PathValue("guid")
	row, err := s.Svc.DB.GetArtifact(ctx, guid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if row == nil {
		writeErr(w, repo.ErrNotFound)
		return
	}
	head, err := s.Svc.Git.Head()
	if err != nil {
		writeErr(w, err)
		return
	}
	entries, err := s.Svc.Git.FileHistory(head, row.RepoPath, 100)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(entries))
	for i, e := range entries {
		out[i] = map[string]any{
			"commit":  e.Hash.String(),
			"message": strings.TrimSpace(e.Message),
			"author":  e.Author,
			"when":    e.When,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": out})
}

func (s *Server) handleOverlay(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.Svc.ResolveOverlay(r.Context(), r.PathValue("guid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resolved)
}

func (s *Server) handleTransition(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[struct {
		To         string `json:"to"`
		IfRevision string `json:"ifRevision"`
	}](w, r)
	if !ok {
		return
	}
	guid := r.PathValue("guid")
	if err := s.Svc.WorkflowTransition(r.Context(), guid, r.PathValue("name"), p.To, p.IfRevision); err != nil {
		writeErr(w, err)
		return
	}
	row, err := s.Svc.DB.GetArtifact(r.Context(), guid)
	if err != nil || row == nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactJSON(row))
}

func (s *Server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[repo.CreateLinkParams](w, r)
	if !ok {
		return
	}
	guid, err := s.Svc.CreateLink(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"guid": guid})
}

func (s *Server) handleUpdateLink(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[repo.UpdateLinkParams](w, r)
	if !ok {
		return
	}
	guid := r.PathValue("guid")
	if err := s.Svc.UpdateLink(r.Context(), guid, p); err != nil {
		writeErr(w, err)
		return
	}
	row, err := s.Svc.DB.GetArtifact(r.Context(), guid)
	if err != nil || row == nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactJSON(row))
}

func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[repo.CreateCommentParams](w, r)
	if !ok {
		return
	}
	guid, err := s.Svc.CreateComment(r.Context(), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"guid": guid})
}

func (s *Server) handleUpdateComment(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[repo.UpdateCommentParams](w, r)
	if !ok {
		return
	}
	guid := r.PathValue("guid")
	if err := s.Svc.UpdateComment(r.Context(), guid, p); err != nil {
		writeErr(w, err)
		return
	}
	row, err := s.Svc.DB.GetArtifact(r.Context(), guid)
	if err != nil || row == nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactJSON(row))
}

// ---- Folder operations ----

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[struct {
		Path string `json:"path"`
	}](w, r)
	if !ok {
		return
	}
	if err := s.Svc.CreateFolder(r.Context(), p.Path); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": strings.Trim(p.Path, "/")})
}

func (s *Server) handleMoveFolder(w http.ResponseWriter, r *http.Request) {
	p, ok := readBody[struct {
		From string `json:"from"`
		To   string `json:"to"`
	}](w, r)
	if !ok {
		return
	}
	if err := s.Svc.MoveFolder(r.Context(), p.From, p.To); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": p.From, "to": p.To})
}

func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	if err := s.Svc.DeleteFolder(r.Context(), r.URL.Query().Get("path")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": r.URL.Query().Get("path")})
}

func (s *Server) handleFolderImpact(w http.ResponseWriter, r *http.Request) {
	impact, err := s.Svc.AnalyzeMove(r.Context(), strings.Trim(r.URL.Query().Get("path"), "/"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, impact)
}
