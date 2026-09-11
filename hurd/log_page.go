package hurd

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

//go:embed logs.js
var logScript string

// Supervisors with a combined log can preserve cross-instance time ordering.
type combinedLogSupervisor interface {
	LogsAll(context.Context, int, io.Writer) error
}

func (o *orchestrator) logInstances() []string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	seen := map[string]bool{}
	for _, row := range o.rows {
		if row.spec.Instance != "" && !row.inProcess && row.res.kind == installedBinary && (row.reg == nil || row.reg.Local) && o.sup.Name() != "in-process" {
			seen[row.spec.Instance] = true
		}
	}
	instances := make([]string, 0, len(seen))
	for instance := range seen {
		instances = append(instances, instance)
	}
	sort.Strings(instances)
	return instances
}

func (o *orchestrator) allLogs(ctx context.Context, w io.Writer) error {
	if sup, ok := o.sup.(combinedLogSupervisor); ok {
		return sup.LogsAll(ctx, 200, w)
	}
	// File-based supervisors have no shared timestamp contract. Keep each tail
	// labelled and grouped instead of pretending to merge them chronologically.
	for _, instance := range o.logInstances() {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(w, "--- %s ---\n", instance)
		if err := o.sup.Logs(ctx, instance, 200, w); err != nil {
			fmt.Fprintf(w, "Could not read this instance: %v\n", err)
		}
		fmt.Fprintln(w)
	}
	return nil
}

// logAvailability only permits the log belonging to a configured local process.
// An in-process or remote registration must not be mistaken for a local unit.
func (o *orchestrator) logAvailability(instance string) (int, error) {
	if instance == "" {
		if _, ok := o.sup.(combinedLogSupervisor); ok {
			return http.StatusOK, nil
		}
		if len(o.logInstances()) > 0 {
			return http.StatusOK, nil
		}
		return http.StatusConflict, fmt.Errorf("No local supervisor logs are available.")
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, row := range o.rows {
		if instance == "" || row.spec.Instance != instance {
			continue
		}
		if row.inProcess || row.res.kind == compiledIn {
			return http.StatusConflict, fmt.Errorf("This device runs inside alpacahurd. Its messages are in alpacahurd's shared log; a separate instance log is not available here.")
		}
		if row.reg != nil && !row.reg.Local {
			return http.StatusConflict, fmt.Errorf("This instance is registered from another host. View its logs on that host.")
		}
		if row.res.kind != installedBinary || o.sup.Name() == "in-process" {
			return http.StatusConflict, fmt.Errorf("No local supervisor log is available for this instance.")
		}
		return http.StatusOK, nil
	}
	return http.StatusNotFound, fmt.Errorf("No configured device instance %q.", instance)
}

func (o *orchestrator) handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Use GET to view logs.", http.StatusMethodNotAllowed)
		return
	}
	instance := r.URL.Query().Get("instance")
	code, err := o.logAvailability(instance)
	if strings.HasSuffix(r.URL.Path, "/tail") {
		w.Header().Set("Content-Type", "application/json")
		var text strings.Builder
		if err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			if instance == "" {
				err = o.allLogs(ctx, &text)
			} else {
				err = o.sup.Logs(ctx, instance, 200, &text)
			}
			if err != nil {
				code = http.StatusBadGateway
			}
		}
		if err != nil {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		serviceState := ""
		if instance != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			status, statusErr := o.sup.Status(ctx, instance)
			cancel()
			serviceState = status.State
			if !status.Installed {
				serviceState = "not installed"
			}
			if statusErr != nil {
				serviceState = "unavailable: " + statusErr.Error()
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": text.String(), "updated": time.Now().UTC().Format(time.RFC3339), "serviceState": serviceState})
		return
	}
	view := struct {
		CSS                                       template.CSS
		Script                                    template.JS
		Instance, Supervisor, Error, Title, Scope string
		Instances                                 []string
	}{CSS: template.CSS(alpacadev.DefaultSetupTemplates().CSS), Script: template.JS(logScript), Instance: instance, Supervisor: o.sup.Name(), Instances: o.logInstances(), Title: instance, Scope: "This instance only · Latest 200 lines"}
	if instance == "" {
		view.Title = "All devices"
		view.Scope = "All local device instances · Latest 200 lines per instance, grouped by instance"
		if _, ok := o.sup.(combinedLogSupervisor); ok {
			view.Scope = "All local device services and alpacahurd · Latest 200 lines in time order"
		}
	}
	if err != nil {
		view.Error = err.Error()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = logPageTemplate.Execute(w, view)
}

var logPageTemplate = template.Must(template.New("logs").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Logs: {{.Title}} — alpacahurd</title><style>{{.CSS}}
body{max-width:90rem}nav{margin-bottom:1.25rem}
#log-filter{display:flex;align-items:center;flex-wrap:wrap;gap:.75rem}
#log-filter label{margin:0}#log-instance{width:auto;max-width:100%;min-width:12rem}
.log-controls{display:flex;flex-wrap:wrap;align-items:center;gap:.75rem;margin:1rem 0}
.log-controls label{display:flex;align-items:center;gap:.5rem;margin:0;min-height:2.75rem}
#log-output{height:60vh;min-height:16rem;overflow:auto;scrollbar-color:#526078 var(--input);margin-top:.5rem}
#log-status{min-height:1.6em}.log-error{color:#efb9b6}
@media(max-width:700px){body{padding:1.25rem 1rem calc(2rem + env(safe-area-inset-bottom))}input,select,textarea{font-size:16px!important}#log-filter{flex-wrap:wrap}#log-instance{width:100%;min-width:0}.log-controls{gap:.5rem}#log-output{height:55vh;min-height:12rem;font-size:.8rem}button{min-height:44px}}
</style></head><body>
<nav aria-label="Back"><a href="/setup">← Devices</a></nav>
<h1>Logs: {{.Title}}</h1>
<form id="log-filter" method="get" action="/setup/logs">
<label for="log-instance">Device instance</label>
<select id="log-instance" name="instance">
<option value=""{{if not .Instance}} selected{{end}}>All devices</option>
{{range .Instances}}<option value="{{.}}"{{if eq . $.Instance}} selected{{end}}>{{.}}</option>{{end}}
</select><button type="submit">View logs</button>
</form>
{{if .Error}}<div class="banner error" role="alert">{{.Error}}</div>{{else}}
<p class="sub">{{.Supervisor}} · {{.Scope}}, refreshed every 2 seconds.<br>A daemon serving multiple devices includes all of their messages.</p>
{{if .Instance}}<p class="sub">{{if eq .Supervisor "systemd"}}Systemd State{{else}}Service State{{end}}: <span id="log-service-state">Checking…</span></p>{{end}}
<div id="log-viewer" data-instance="{{.Instance}}">
<div class="log-controls">
<button id="log-pause" type="button" disabled>Pause</button>
<button id="log-refresh" type="button" disabled>Refresh now</button>
<label><input id="log-follow" type="checkbox" checked>Follow latest</label>
</div>
<p id="log-status" class="sub" role="status" aria-live="polite">Loading logs…</p>
<pre id="log-output" class="result" tabindex="0" aria-label="Instance log"></pre>
<p class="help">Pause to read a fixed snapshot. Turn off Follow latest to keep your scroll position. Auto-refresh waits while this tab is hidden. This view refreshes a recent snapshot; it is not a complete streaming history.</p>
<noscript><p>Enable JavaScript to load and refresh logs.</p></noscript>
</div>
{{end}}
<script>{{.Script}}</script>
</body></html>`))
