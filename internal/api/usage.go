package api

import (
	"net/http"
	"time"
)

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 30)
	if days < 1 || days > 365 {
		writeError(w, http.StatusBadRequest, "invalid_argument", "days must be between 1 and 365")
		return
	}
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "unknown timezone "+tz)
		return
	}
	// Start at local midnight so the first day is complete.
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	u, err := s.Store.UsageReport(r.Context(), start, tz)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}
