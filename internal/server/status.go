package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/singhsays/tidepull/internal/model"
	"github.com/singhsays/tidepull/internal/provider"
	"github.com/singhsays/tidepull/internal/state"
	"github.com/singhsays/tidepull/internal/version"
)

// templates are parsed once; "page" is the full document, "fragment" is the
// element htmx swaps when re-requesting the page.
var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// accountRow is one account's rendered status.
type accountRow struct {
	Name          string
	Provider      string
	Icon          template.HTML
	Identity      string
	RemoteURL    string
	LocalDir      string
	FolderID      string
	FolderDisplay string
	Connected     bool
	HasStatus     bool
	LastCycleAt   string
	LastOKAt      string
	OK            bool
	LastError     string
	FileCount     int
	DurationMS    int64
	Adds          int
	Updates       int
	Removes       int
	TokenAge      string
}

// handleStatus serves the status page, or only the htmx-swapped fragment
// when the request comes from htmx.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	rows, err := s.buildRows()
	if err != nil {
		http.Error(w, fmt.Sprintf("load cycle status: %v", err), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := templates.ExecuteTemplate(w, "fragment", rows); err != nil {
			log.Logger.Error().Err(err).Str("path", r.URL.Path).Msg("render status fragment")
		}
		return
	}
	data := statusPageData{
		Rows:    rows,
		Version: version.String(),
		Uptime:  time.Since(s.startedAt).Round(time.Second).String(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "page.html", data); err != nil {
		log.Logger.Error().Err(err).Str("path", r.URL.Path).Msg("render status page")
	}
}

// statusPageData renders the full status page.
type statusPageData struct {
	Rows    []accountRow
	Version string
	Uptime  string
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
		row := accountRow{Name: acct.Name, Provider: acct.Provider, LocalDir: acct.LocalDir, FolderID: acct.FolderID}
		row.RemoteURL = provider.RemoteURL(acct.Provider, acct.FolderID)
		row.FolderDisplay = acct.FolderID
		if row.FolderDisplay == "" {
			row.FolderDisplay = "account root"
		}
		if spec, err := s.specFor(acct.Provider); err == nil {
			row.Icon = template.HTML(spec.Icon)
		}
		if id, err := s.db.MetaLoad(acct.Name, metaIdentity); err == nil {
			var ident model.AccountIdentity
			if json.Unmarshal([]byte(id), &ident) == nil {
				switch {
				case ident.Email != "":
					row.Identity = ident.Email
				case ident.Name != "":
					row.Identity = ident.Name
				}
			}
		}
		if updated, err := s.db.TokenUpdatedAt(acct.Name); err == nil && updated != "" {
			row.TokenAge = relAge(updated)
		}
		if _, err := s.db.TokenLoad(acct.Name); err == nil {
			row.Connected = true
		} else if !errors.Is(err, state.ErrNoToken) {
			return nil, err
		}
		if st, ok := byAccount[acct.Name]; ok {
			row.HasStatus = true
			row.OK = st.Outcome == state.OutcomeOK
			row.LastCycleAt = relAge(st.LastCycleAt)
			row.LastOKAt = relAge(st.LastOKAt)
			row.LastError = st.LastError
			row.FileCount = st.FileCount
			row.DurationMS = st.DurationMS
			row.Adds, row.Updates, row.Removes = st.Adds, st.Updates, st.Removes
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// metaIdentity is the meta key holding the authenticated account's name
// as JSON; the engine writes it, the page reads it.
const metaIdentity = "identity"

// relAge renders an RFC3339 UTC timestamp as a coarse relative age
// ("3h"), without a qualifier; callers phrase it as "ago" or "old".
// falling back to the raw value when it does not parse.
func relAge(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
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
