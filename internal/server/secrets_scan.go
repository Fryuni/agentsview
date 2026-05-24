package server

import (
	"net/http"

	"go.kenn.io/agentsview/internal/service"
)

// handleScanSecrets streams a secret scan as SSE: "progress" ticks, then a
// final "summary", or an "error" event. Unavailable in read-only/remote mode.
func (s *Server) handleScanSecrets(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusNotImplemented, "not available in remote mode")
		return
	}
	q := r.URL.Query()
	dateFrom := q.Get("date_from")
	dateTo := q.Get("date_to")
	if !validateDateFilters(w, "", dateFrom, dateTo, "") {
		return
	}
	in := service.SecretScanInput{
		Backfill: q.Get("backfill") == "true",
		Project:  q.Get("project"),
		Agent:    q.Get("agent"),
		DateFrom: dateFrom,
		DateTo:   dateTo,
	}
	stream, err := NewSSEStream(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	sum, err := s.sessions.ScanSecrets(r.Context(), in,
		func(p service.SecretScanProgress) { stream.SendJSON("progress", p) })
	if err != nil {
		stream.Send("error", err.Error())
		return
	}
	stream.SendJSON("summary", sum)
}
