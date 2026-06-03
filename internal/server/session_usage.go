package server

import (
	"net/http"

	"go.kenn.io/agentsview/internal/db"
)

type sessionUsageResponse struct {
	db.SessionUsage
	UnpricedModels []string `json:"unpriced_models"`
	ServerRunning  bool     `json:"server_running"`
}

type sessionUsageErrorResponse struct {
	Error sessionUsageError `json:"error"`
}

type sessionUsageError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleSessionUsage(
	w http.ResponseWriter, r *http.Request,
) {
	usage, err := s.db.GetSessionUsage(r.Context(), r.PathValue("id"))
	if err != nil {
		if handleContextError(w, err) {
			return
		}
		writeSessionUsageError(
			w,
			http.StatusInternalServerError,
			"usage_query_failed",
			"failed to query session usage",
		)
		return
	}
	if usage == nil {
		writeSessionUsageError(
			w,
			http.StatusNotFound,
			"session_not_found",
			"session not found",
		)
		return
	}

	writeJSON(w, http.StatusOK, newSessionUsageResponse(usage))
}

func newSessionUsageResponse(usage *db.SessionUsage) sessionUsageResponse {
	unpricedModels := usage.UnpricedModels
	if unpricedModels == nil {
		unpricedModels = []string{}
	}
	return sessionUsageResponse{
		SessionUsage:   *usage,
		UnpricedModels: unpricedModels,
		ServerRunning:  true,
	}
}

func writeSessionUsageError(
	w http.ResponseWriter,
	status int,
	code string,
	message string,
) {
	writeJSON(w, status, sessionUsageErrorResponse{
		Error: sessionUsageError{
			Code:    code,
			Message: message,
		},
	})
}
