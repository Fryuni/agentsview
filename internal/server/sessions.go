package server

import (
	"errors"
	"net/http"
	"strconv"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/service"
)

func (s *Server) handleListSessions(
	w http.ResponseWriter, r *http.Request,
) {
	q := r.URL.Query()

	// HTTP-layer validation produces cleaner 400 responses for web
	// clients. The service layer re-validates as a second line of
	// defense; duplication is intentional.
	limit, ok := parseIntParam(w, r, "limit")
	if !ok {
		return
	}
	// Clamp at the HTTP layer to preserve test-observable behavior
	// (e.g. limit=1000 returns 500, not the default). The service
	// also clamps, so setting filter.Limit=limit makes that a no-op.
	limit = clampLimit(limit, db.DefaultSessionLimit, db.MaxSessionLimit)

	minMsgs, ok := parseIntParam(w, r, "min_messages")
	if !ok {
		return
	}
	maxMsgs, ok := parseIntParam(w, r, "max_messages")
	if !ok {
		return
	}
	minUserMsgs, ok := parseIntParam(w, r, "min_user_messages")
	if !ok {
		return
	}

	date := q.Get("date")
	dateFrom := q.Get("date_from")
	dateTo := q.Get("date_to")
	activeSince := q.Get("active_since")
	if !validateDateFilters(w, date, dateFrom, dateTo, activeSince) {
		return
	}

	filter := service.ListFilter{
		Project:          q.Get("project"),
		ExcludeProject:   q.Get("exclude_project"),
		Machine:          q.Get("machine"),
		Agent:            q.Get("agent"),
		Date:             date,
		DateFrom:         dateFrom,
		DateTo:           dateTo,
		ActiveSince:      activeSince,
		MinMessages:      minMsgs,
		MaxMessages:      maxMsgs,
		MinUserMessages:  minUserMsgs,
		IncludeOneShot:   q.Get("include_one_shot") == "true",
		IncludeAutomated: q.Get("include_automated") == "true",
		IncludeChildren:  q.Get("include_children") == "true",
		Outcome:          q.Get("outcome"),
		HealthGrade:      q.Get("health_grade"),
		Cursor:           q.Get("cursor"),
		Limit:            limit,
		Termination:      q.Get("termination"),
	}
	if v := q.Get("min_tool_failures"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest,
				"invalid min_tool_failures parameter")
			return
		}
		filter.MinToolFailures = &n
	}
	filter.HasSecret = q.Get("has_secret") == "true"

	page, err := s.sessions.List(r.Context(), filter)
	if err != nil {
		if handleContextError(w, err) {
			return
		}
		if errors.Is(err, db.ErrInvalidCursor) {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleGetSession(
	w http.ResponseWriter, r *http.Request,
) {
	id := r.PathValue("id")
	detail, err := s.sessions.Get(r.Context(), id)
	if err != nil {
		if handleContextError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if detail == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleGetChildSessions(
	w http.ResponseWriter, r *http.Request,
) {
	id := r.PathValue("id")
	children, err := s.db.GetChildSessions(r.Context(), id)
	if err != nil {
		if handleContextError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if children == nil {
		children = []db.Session{}
	}
	writeJSON(w, http.StatusOK, children)
}

// handleSearchSession handles GET /api/v1/sessions/{id}/search?q=...
// Returns matching message ordinals in document order.
func (s *Server) handleSearchSession(
	w http.ResponseWriter, r *http.Request,
) {
	id := r.PathValue("id")
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ordinals": []int{}})
		return
	}
	ordinals, err := s.db.SearchSession(r.Context(), id, q)
	if err != nil {
		if handleContextError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ordinals == nil {
		ordinals = []int{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ordinals": ordinals})
}
