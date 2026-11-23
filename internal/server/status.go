package server

import (
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/singhsays/tidepull/internal/state"
)

// templates are parsed once; "page" is the full document, "fragment" is the
// element htmx swaps when re-requesting the page.
var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// statusTimeLayout is the display format for a cycle's UTC timestamp.
const statusTimeLayout = "2006-01-02 15:04:05 MST"

// accountRow is one account's rendered status.
type accountRow struct {
	Name        string
	Provider    string
	HasStatus   bool
	LastCycleAt string
	OK          bool
	LastError   string
	FileCount   int
}

// handleStatus serves the status page, or only the htmx-swapped fragment
// when the request comes from htmx.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	rows, err := s.buildRows()
	if err != nil {
		http.Error(w, fmt.Sprintf("load cycle status: %v", err), http.StatusInternalServerError)
		return
	}
	tmplName := "page.html"
	if r.Header.Get("HX-Request") == "true" {
		tmplName = "fragment"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, tmplName, rows); err != nil {
		log.Logger.Error().Err(err).Str("path", r.URL.Path).Msg("render status page")
	}
}

// buildRows joins the configured accounts with their latest cycle status.
// Configured accounts without a status row render as not synced yet.
func (s *Server) buildRows() ([]accountRow, error) {
	statuses, err := s.db.CycleStatusLoadAll()
	if err != nil {
		return nil, err
	}
	byAccount := make(map[string]state.CycleStatus, len(statuses))
	for _, st := range statuses {
		byAccount[st.Account] = st
	}
	rows := make([]accountRow, 0, len(s.cfg.Accounts))
	for _, acct := range s.cfg.Accounts {
		row := accountRow{Name: acct.Name, Provider: acct.Provider}
		if st, ok := byAccount[acct.Name]; ok {
			row.HasStatus = true
			row.OK = st.Outcome == state.OutcomeOK
			row.LastCycleAt = formatCycleTime(st.LastCycleAt)
			row.LastError = st.LastError
			row.FileCount = st.FileCount
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// formatCycleTime renders an RFC3339 UTC timestamp for display, falling back
// to the raw value when it does not parse.
func formatCycleTime(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format(statusTimeLayout)
}

// handleHealthz serves the liveness probe.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

// handleHTMX serves the embedded htmx script.
func handleHTMX(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFileFS(w, r, assetsFS, "assets/htmx.min.js")
}
