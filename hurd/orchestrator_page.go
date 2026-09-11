package hurd

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// orchestrator holds the setup page state, protected by mu.
type orchestrator struct {
	cfgPath   string
	cfg       *Config
	sup       Supervisor
	mu        sync.RWMutex
	rows      []orchRow
	servers   map[int]*alpacadev.Server // by bound port
	startedAt time.Time
	page      int // the port the orchestrator page bound; 0 until it does
	// extra holds unconfigured registrations by UniqueID or address:port.
	extra map[string]*alpacadev.Registration

	ctx         context.Context
	listenAddrs []string
	logger      *log.Logger
	byPort      map[int]*alpacadev.Server
	nums        map[int]*deviceNumbers
	scanCount   int
	resp        *responder
}

// orchRow is one device on the page.
type orchRow struct {
	spec       DeviceSpec
	res        resolution
	num        int
	port       int  // bound port for an in-process device; the file's for a binary
	inProcess  bool // served by this orchestrator's own process
	skipped    string
	deviceName string
	devType    alpacadev.DeviceType
	srvKey     int // key into serve's byPort map, to read the bound port back
	// reg is the latest heartbeat matching this instance, or nil.
	reg *alpacadev.Registration
	// reloadable indicates support for in-process reload.
	reloadable bool
	// stopFrontEnd cancels the optional driver front-end.
	stopFrontEnd context.CancelFunc
}

// noteRegistration updates the matching instance or records an extra device.
func (o *orchestrator) noteRegistration(e *alpacadev.Registration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := range o.rows {
		if e.Instance != "" && o.rows[i].spec.Instance == e.Instance {
			o.rows[i].reg = e
			return
		}
	}
	if o.extra == nil {
		o.extra = map[string]*alpacadev.Registration{}
	}
	key := e.UniqueID
	if key == "" {
		key = fmt.Sprintf("%s:%d", e.Addr, e.AlpacaPort)
	}
	o.extra[key] = e
}

// registeredState describes a heartbeat, or returns empty for an expired registration.
func registeredState(e *alpacadev.Registration) string {
	if e == nil {
		return ""
	}
	age := time.Since(e.Seen)
	if age > alpacadev.DefaultRegistrationTTL {
		return ""
	}
	where := "this host"
	if !e.Local {
		where = e.Addr.String()
	}
	return fmt.Sprintf("registered from %s, %s ago", where, age.Round(time.Second))
}

// restart restarts an installed service or recreates every running device block
// for an in-process instance. It never restarts the orchestrator itself.
func (o *orchestrator) restart(ctx context.Context, instance string) error {
	o.mu.RLock()
	var rows []orchRow
	for _, row := range o.rows {
		if instance != "" && row.spec.Instance == instance {
			rows = append(rows, row)
		}
	}
	type target struct {
		srv *alpacadev.Server
		typ alpacadev.DeviceType
		num int
	}
	var targets []target
	for _, row := range rows {
		if row.inProcess {
			targets = append(targets, target{o.servers[row.port], row.devType, row.num})
		}
	}
	o.mu.RUnlock()
	if len(rows) == 0 {
		return fmt.Errorf("no configured device %q to restart", instance)
	}
	if !rows[0].spec.enabled() {
		return fmt.Errorf("%s is disabled; enable it before restarting", instance)
	}
	if len(targets) > 0 {
		for _, t := range targets {
			if t.srv == nil {
				return fmt.Errorf("%s has no running device server", instance)
			}
			if err := t.srv.Reload(ctx, t.typ, t.num); err != nil {
				return err
			}
		}
		return nil
	}
	if rows[0].res.kind != installedBinary {
		return fmt.Errorf("%s is not running; enable it to start it", instance)
	}
	if rows[0].reg != nil && !rows[0].reg.Local {
		return fmt.Errorf("%s runs on another host; restart it there", instance)
	}
	return o.sup.Restart(ctx, instance)
}

// reload reloads a configured instance or an unconfigured registration by uniqueID.
func (o *orchestrator) reload(ctx context.Context, inst, uniqueID string) error {
	o.mu.RLock()
	var row *orchRow
	for i := range o.rows {
		if inst != "" && inst != "(inline)" && o.rows[i].spec.Instance == inst {
			row = &o.rows[i]
			break
		}
	}
	var reg *alpacadev.Registration
	var typ alpacadev.DeviceType
	var num, port int
	var srv *alpacadev.Server
	switch {
	case row != nil && row.inProcess:
		srv, typ, num, port = o.servers[row.port], row.devType, row.num, row.port
	case row != nil && registeredState(row.reg) != "":
		reg = row.reg
	case uniqueID != "":
		reg = o.extra[uniqueID]
	}
	o.mu.RUnlock()

	switch {
	case srv != nil:
		return srv.Reload(ctx, typ, num)
	case reg != nil && registeredState(reg) != "":
		return reloadOverHTTP(ctx, reg)
	case row != nil:
		return fmt.Errorf("%s is not running as a process this page can reach (port %d)", inst, port)
	}
	return fmt.Errorf("no device %q to reload", inst)
}

// reloadOverHTTP posts the reload form to a separate binary's device page. A
// device binary registers its one device as number 0.
func reloadOverHTTP(ctx context.Context, reg *alpacadev.Registration) error {
	u := fmt.Sprintf("http://%s/setup/v1/%s/0/setup", net.JoinHostPort(reg.Addr.String(), fmt.Sprint(reg.AlpacaPort)), strings.ToLower(reg.DeviceType))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(url.Values{"_form": {"reload"}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	if strings.Contains(string(body), "Reload failed") {
		return fmt.Errorf("%s reported: %s", u, textBetween(string(body), "Reload failed: ", "<"))
	}
	if !strings.Contains(string(body), "Reloaded") {
		return fmt.Errorf("%s did not offer a reload; the device has no reloader", u)
	}
	return nil
}

// textBetween returns the text of s after the first from up to the next to.
func textBetween(s, from, to string) string {
	_, after, ok := strings.Cut(s, from)
	if !ok {
		return ""
	}
	before, _, _ := strings.Cut(after, to)
	return before
}

// deviceTypeOf is the ASCOM type the device registered as.
func deviceTypeOf(spec DeviceSpec, dev alpacadev.Device) alpacadev.DeviceType {
	if drv, ok := registry.Lookup(spec.Driver); ok {
		return drv.Type
	}
	return ""
}

// setupPages returns the orchestrator setup, check, add, and edit pages.
func (o *orchestrator) setupPages() map[string]http.Handler {
	return map[string]http.Handler{"check": o, "add": o, "edit": o, "logs": o, "readiness": o}
}

// redirectPages links device servers to the orchestrator using the requested host.
func (o *orchestrator) redirectPages() map[string]http.Handler {
	return map[string]http.Handler{"hurd": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := o.pagePort(); p != 0 {
			http.Redirect(w, r, fmt.Sprintf("http://%s:%d/setup", hostOf(r), p), http.StatusFound)
			return
		}
		http.Error(w, "the orchestrator page is turned off (setupPort -1)", http.StatusNotFound)
	})}
}

// setPagePort records the port the orchestrator page bound; pagePort reads it.
func (o *orchestrator) setPagePort(p int) {
	o.mu.Lock()
	o.page = p
	o.mu.Unlock()
}

func (o *orchestrator) pagePort() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.page
}

func (o *orchestrator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/setup")
	switch {
	case rest == "/readiness":
		o.handleReadiness(w, r)
	case rest == "" || rest == "/":
		if r.Method == http.MethodPost {
			o.handleAction(w, r)
			return
		}
		o.render(w, r, "", "")
	case rest == "/check":
		var b strings.Builder
		fatal, errs := checkConfig(&b, o.cfg)
		kind := "ok"
		if fatal+errs > 0 {
			kind = "error"
		}
		o.render(w, r, b.String(), kind)
	case rest == "/add":
		if r.Method == http.MethodPost {
			o.handleAdd(w, r)
			return
		}
		o.render(w, r, "", "")
	case rest == "/edit/check":
		o.handleEditCheck(w, r)
	case rest == "/logs", rest == "/logs/tail":
		o.handleLogs(w, r)
	case rest == "/edit":
		o.handleEdit(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleAction runs one supervisor action on one instance and re-renders.
func (o *orchestrator) handleAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		o.render(w, r, "could not read the form", "error")
		return
	}
	inst, action := r.PostForm.Get("instance"), r.PostForm.Get("action")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var err error
	switch action {
	case "delete":
		err = o.deleteDevice(ctx, inst)
		if err == nil {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
	case "restart", "reload":
		action = "restart"
		err = o.restart(ctx, inst)
	case "logs":
		http.Redirect(w, r, "/setup/logs?"+url.Values{"instance": {inst}}.Encode(), http.StatusSeeOther)
		return
	case "start", "stop":
		// Compiled-in drivers cannot be launched through the supervisor.
		if kind, known := o.resolutionKind(inst); known && kind != installedBinary {
			err = fmt.Errorf("%s: its driver is %s, not a separate binary; the enable/disable switch runs it", inst, kind)
			break
		}
		switch action {
		case "start":
			spec, checkErr := o.editableSpec(inst)
			if checkErr == nil {
				var data []byte
				data, checkErr = os.ReadFile(spec.Source)
				if checkErr == nil {
					var warning string
					spec, warning, checkErr = validateDeviceEdit(ctx, spec, string(data))
					if checkErr == nil && (!spec.enabled() || warning != "") {
						checkErr = fmt.Errorf("enable the device and complete its configuration before starting it")
					}
				}
			}
			if checkErr != nil {
				err = checkErr
			} else {
				err = o.sup.Start(ctx, inst)
			}
		case "stop":
			err = o.sup.Stop(ctx, inst)
		}
	case "enable", "disable":
		ectx, ecancel := context.WithTimeout(r.Context(), enableTimeout)
		defer ecancel()
		msg, err := o.setEnabled(ectx, inst, action == "enable")
		if err != nil {
			o.render(w, r, fmt.Sprintf("%s %s: %v", action, inst, err), "error")
			return
		}
		o.render(w, r, msg, "ok")
		return
	default:
		err = fmt.Errorf("unknown action %q", action)
	}
	if err != nil {
		o.render(w, r, fmt.Sprintf("%s %s: %v", action, inst, err), "error")
		return
	}
	o.render(w, r, fmt.Sprintf("%s %s: done", action, inst), "ok")
}

// resolutionKind reports how inst's driver resolves, when the table knows the
// instance.
func (o *orchestrator) resolutionKind(inst string) (resolutionKind, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for i := range o.rows {
		if o.rows[i].spec.Instance == inst {
			return o.rows[i].res.kind, true
		}
	}
	return unresolved, false
}

// handleAdd writes a disabled device file from a compiled-in or installed driver schema.
func (o *orchestrator) handleAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		o.render(w, r, "could not read the form", "error")
		return
	}
	driver, inst := r.PostForm.Get("driver"), strings.TrimSpace(r.PostForm.Get("instance"))
	if inst == "" || strings.ContainsAny(inst, `/\`) || strings.HasPrefix(inst, ".") {
		o.render(w, r, "the instance name must be a plain filename stem", "error")
		return
	}
	dir := devicesDirFor(o.cfgPath)
	path := filepath.Join(dir, inst+".json")
	if _, err := os.Stat(path); err == nil {
		o.render(w, r, fmt.Sprintf("%s exists already", path), "error")
		return
	}
	// Do not let new configurations inherit unrelated settings from stale state.
	if _, err := os.Stat(filepath.Join(stateDevicesDir(), inst+".json")); err == nil {
		o.render(w, r, "This instance name already has saved state. Choose a new name to create a disabled device.", "error")
		return
	} else if !os.IsNotExist(err) {
		o.render(w, r, err.Error(), "error")
		return
	}
	text, err := newDeviceTemplate(r.Context(), o.cfgPath, driver)
	if err != nil {
		o.render(w, r, err.Error(), "error")
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		o.render(w, r, err.Error(), "error")
		return
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		o.render(w, r, fmt.Sprintf("write %s: %v", path, err), "error")
		return
	}
	if spec, err := loadDeviceFile(path, stateDevicesDir()); err == nil {
		o.mu.Lock()
		o.rows = append(o.rows, orchRow{spec: spec, res: resolveDriver(spec), port: spec.Port})
		o.cfg.Devices = append(o.cfg.Devices, spec)
		o.mu.Unlock()
	}
	http.Redirect(w, r, "/setup/edit?"+url.Values{"instance": {inst}, "created": {"1"}}.Encode(), http.StatusSeeOther)
}

// handleEdit displays or validates and saves a known instance configuration file.
func (o *orchestrator) handleEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		o.render(w, r, "", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		o.render(w, r, "could not read the form", "error")
		return
	}
	inst, text := r.PostForm.Get("instance"), r.PostForm.Get("text")
	original, err := o.editableSpec(inst)
	if err != nil {
		o.render(w, r, err.Error(), "error")
		return
	}
	spec, warning, err := validateDeviceEdit(r.Context(), original, text)
	if err != nil {
		o.render(w, r, fmt.Sprintf("%s not saved: %v", inst, err), "error")
		return
	}
	o.mu.Lock()
	var row *orchRow
	for i := range o.rows {
		if o.rows[i].spec.Instance == inst && o.rows[i].spec.Source == original.Source {
			row = &o.rows[i]
			break
		}
	}
	if row == nil {
		o.mu.Unlock()
		o.render(w, r, "Device configuration changed; check the draft again.", "error")
		return
	}
	path := original.Source
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if err := writeFileAtomic(path, []byte(text)); err != nil {
		o.mu.Unlock()
		o.render(w, r, fmt.Sprintf("%s not saved: %v", inst, err), "error")
		return
	}
	row.spec = spec
	row.res = resolveDriver(spec)
	if !row.inProcess {
		row.skipped = ""
	}
	for i := range o.cfg.Devices {
		if o.cfg.Devices[i].Instance == inst {
			o.cfg.Devices[i] = spec
		}
	}
	o.mu.Unlock()
	query := url.Values{"saved": {inst}}
	if warning != "" {
		query.Set("draft", "1")
	}
	http.Redirect(w, r, "/setup?"+query.Encode(), http.StatusSeeOther)
}

// writeFileAtomic replaces path through a temporary file and rename.
func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// devicePageRow uses reported identity first, then configured values. A heartbeat
// describes a daemon but does not report an Alpaca device number.
func devicePageRow(row orchRow) pageRow {
	pr := pageRow{Instance: row.spec.Instance, Driver: row.spec.Driver,
		Type: string(row.devType), Name: row.deviceName, Num: "Automatic",
		Skipped: row.skipped, Enabled: row.spec.enabled()}
	if row.inProcess {
		pr.Num = fmt.Sprint(row.num)
	} else if row.spec.Device != nil {
		pr.Num = fmt.Sprint(*row.spec.Device)
	}
	if !row.inProcess && registeredState(row.reg) != "" {
		pr.Type = row.reg.DeviceType
		pr.Name = row.reg.DeviceName
	}
	if pr.Type == "" {
		if drv, ok := registry.Lookup(row.spec.Driver); ok {
			pr.Type = string(drv.Type)
		}
	}
	pr.Type = displayValue(pr.Type, "Unknown")
	pr.Name = displayValue(pr.Name, row.spec.Name)
	pr.Name = displayValue(pr.Name, row.spec.Instance)
	pr.Name = displayValue(pr.Name, row.spec.Driver)
	return pr
}

func displayValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// pageRow is one rendered row.
type pageRow struct {
	Instance, Driver, Type, Name, How, Port, State, Setup, Argv, Skipped string
	Num                                                                  string
	ServiceState                                                         string
	Enabled, Running, Actions                                            bool
	// Reload controls the reload button. UniqueID identifies an unconfigured registration.
	Reload   bool
	UniqueID string
	// Toggle enables the in-process device switch.
	Toggle bool
	// Edit links the raw device file editor for a devices.d entry.
	Edit bool
}

type pageView struct {
	CSS        template.CSS
	ConfigDir  string
	StateDir   string
	Supervisor string
	Uptime     string
	Version    string
	Port       int // the port this page is served on
	Rows       []pageRow
	Drivers    []string
	Banner     string
	BannerKind string
	CheckOut   string
	CheckKind  string
	// EditPage selects the dedicated editor layout, including editor errors.
	AddPage      bool
	AddDriver    string
	AddInstance  string
	EditPage     bool
	EditInstance string
	EditPath     string
	EditText     string
	EditorJS     template.JS
	ReadinessJS  template.JS
}

func (o *orchestrator) render(w http.ResponseWriter, r *http.Request, banner, kind string) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	view := pageView{
		ConfigDir:   filepath.Dir(o.cfgPath),
		StateDir:    stateDirRoot(),
		Supervisor:  o.sup.Name(),
		Version:     version,
		ReadinessJS: template.JS(readinessJS),
		Port:        o.page,
		Uptime:      time.Since(o.startedAt).Round(time.Second).String(),
		EditPage:    strings.HasSuffix(r.URL.Path, "/edit"),
		AddPage:     strings.HasSuffix(r.URL.Path, "/add"),
	}
	if strings.HasSuffix(r.URL.Path, "/check") || kind == "output" {
		view.CheckOut, view.CheckKind = banner, kind
	} else {
		view.Banner, view.BannerKind = banner, kind
	}
	inst := r.URL.Query().Get("instance")
	if r.Method == http.MethodPost && view.EditPage {
		inst = r.PostForm.Get("instance")
	}
	if inst != "" && view.EditPage {
		view.EditorJS = template.JS(editorScript) // embedded source, never user input

		for _, row := range o.rows {
			if row.spec.Instance == inst && row.spec.Source != "" {
				view.EditInstance, view.EditPath = inst, row.spec.Source
				if r.Method == http.MethodPost {
					view.EditText = r.PostForm.Get("text")
				} else if b, err := os.ReadFile(row.spec.Source); err == nil {
					view.EditText = string(b)
				} else {
					view.EditText = "// " + err.Error()
				}
			}
		}
	}
	view.CSS = template.CSS(alpacadev.DefaultSetupTemplates().CSS)
	if view.AddPage {
		known := map[string]bool{}
		for _, d := range registry.All() {
			known[d.Name] = true
		}
		installed, err := installedDrivers(o.cfgPath)
		for name := range installed {
			known[name] = true
		}
		for name := range known {
			view.Drivers = append(view.Drivers, name)
		}
		sort.Strings(view.Drivers)
		if err != nil && view.Banner == "" {
			view.Banner, view.BannerKind = err.Error(), "warn"
		}
		if r.Method == http.MethodPost {
			view.AddDriver, view.AddInstance = r.PostForm.Get("driver"), r.PostForm.Get("instance")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = orchTmpl.Execute(w, view)
		return
	}
	if view.EditPage {
		if view.EditInstance != "" && r.Method == http.MethodGet && r.URL.Query().Get("created") == "1" {
			view.Banner, view.BannerKind = "Created "+view.EditInstance+". Configure it below; it remains disabled until you enable it from the device list.", "ok"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if view.EditInstance == "" && view.Banner == "" {
			view.Banner, view.BannerKind = "Device configuration not found.", "error"
			w.WriteHeader(http.StatusNotFound)
		}
		_ = orchTmpl.Execute(w, view)
		return
	}
	if saved := r.URL.Query().Get("saved"); saved != "" && r.Method == http.MethodGet && view.Banner == "" {
		for _, row := range o.rows {
			if row.spec.Instance != saved || row.spec.Source == "" {
				continue
			}
			view.Banner, view.BannerKind = fmt.Sprintf("saved %s", row.spec.Source), "ok"
			if row.inProcess || registeredState(row.reg) != "" {
				view.Banner += "; restart the device to apply it"
			} else if row.spec.enabled() {
				view.Banner += "; enable the device to start it"
			} else {
				view.Banner += "; the device remains disabled"
				if r.URL.Query().Get("draft") == "1" {
					view.Banner += ". Draft saved; driver configuration still needs attention before enabling."
					view.BannerKind = "warn"
				}
			}
			break
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	for _, row := range o.rows {
		pr := devicePageRow(row)

		if pr.Instance == "" {
			pr.Instance = "(inline)"
		}
		pr.Edit = row.spec.Instance != "" && row.spec.Source != ""
		if row.res.kind == installedBinary {
			pr.Argv = row.res.exe + " " + strings.Join(row.res.args, " ")
		}
		regState := registeredState(row.reg)
		switch {
		case !row.spec.enabled():
			pr.How = row.res.kind.String()
			if row.port != 0 {
				pr.Port = fmt.Sprint(row.port)
			}
			pr.Toggle = row.spec.Instance != "" && row.res.kind == compiledIn
			pr.Actions = row.spec.Instance != "" && row.res.kind == installedBinary
		case regState != "":
			pr.How, pr.State, pr.Running = "separate binary", "enabled", true
			pr.Port = fmt.Sprint(row.reg.AlpacaPort)
			host := hostOf(r)
			if !row.reg.Local {
				host = row.reg.Addr.String()
			}
			pr.Setup = fmt.Sprintf("http://%s:%d/setup", host, row.reg.AlpacaPort)
			pr.Actions = row.skipped == ""
			pr.Reload = true
		case row.skipped != "":
			pr.How, pr.State = row.res.kind.String(), "skipped"
			if row.port != 0 {
				pr.Port = fmt.Sprint(row.port)
			}
			pr.Toggle = row.spec.Instance != ""
		case row.inProcess:
			pr.How, pr.State, pr.Running = "in process", "running", true
			pr.Port = fmt.Sprint(row.port)
			pr.Setup = fmt.Sprintf("http://%s:%d/setup/v1/%s/%d/setup", hostOf(r), row.port, row.devType, row.num)
			pr.Reload = row.reloadable
			pr.Toggle = row.spec.Instance != ""
		default:
			if row.res.kind != installedBinary {
				pr.How, pr.State = row.res.kind.String(), "not running"
				if row.port != 0 {
					pr.Port = fmt.Sprint(row.port)
				}
				pr.Toggle = row.spec.Instance != ""
				break
			}
			pr.How = "separate binary"
			pr.Actions = true
			if row.port != 0 {
				pr.Port = fmt.Sprint(row.port)
				pr.Setup = fmt.Sprintf("http://%s:%d/setup", hostOf(r), row.port)
			}
			pr.State = "enabled"
		}
		pr.ServiceState = "Not applicable"
		_, noService := o.sup.(noSupervisor)
		if row.res.kind == installedBinary && row.spec.Instance != "" && !noService && (row.reg == nil || row.reg.Local) {
			st, err := o.sup.Status(ctx, row.spec.Instance)
			if err != nil {
				pr.ServiceState = "status: " + err.Error()
			} else {
				pr.ServiceState = displayValue(st.State, "Unknown")
				pr.Running = pr.Running || st.Running
				if !st.Installed {
					pr.ServiceState = "not installed"
				}
			}
		}

		view.Rows = append(view.Rows, pr)
	}
	var extraKeys []string
	for k := range o.extra {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		e := o.extra[k]
		state := registeredState(e)
		if state == "" {
			continue
		}
		host := hostOf(r)
		if !e.Local {
			host = e.Addr.String()
		}
		inst := e.Instance
		if inst == "" {
			inst = "(unconfigured)"
		}
		view.Rows = append(view.Rows, pageRow{
			Instance: inst, Type: displayValue(e.DeviceType, "Unknown"), Name: displayValue(e.DeviceName, inst), Num: "Unknown",
			How: "registered", State: state, ServiceState: "Not applicable", Enabled: true, Running: true, Port: fmt.Sprint(e.AlpacaPort),
			Setup:  fmt.Sprintf("http://%s:%d/setup", host, e.AlpacaPort),
			Reload: true, UniqueID: e.UniqueID,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = orchTmpl.Execute(w, view)
}

// hostOf returns the requested host without its port.
func hostOf(r *http.Request) string {
	h := r.Host
	if u, err := url.Parse("http://" + h); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return h
}

var orchTmpl = template.Must(template.New("hurd").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{if .EditPage}}Edit {{.EditInstance}} — {{else if .AddPage}}Add device — {{end}}alpacahurd</title><style>{{.CSS}}
body{max-width:90rem}
.readiness-label{display:inline-block;padding:.2rem .55rem;border:1px solid #536071;border-radius:.35rem;white-space:nowrap;font-size:.9rem}
.readiness-label.ok{color:#b6dfc0;background:#1d3025;border-color:#50775b}
.readiness-label.warn{color:#edd395;background:#342c1b;border-color:#806936}
.readiness-label.error{color:#efb6b6;background:#352224;border-color:#805052}

body>form{max-width:52rem}
body.editor-page{max-width:64rem}
.editor-page>form{max-width:none}
.editor-page nav{margin-bottom:1.25rem}
.page-actions{display:flex;flex-wrap:wrap;gap:.5rem 1.5rem;margin:.5rem 0 1rem}
.page-actions a{display:inline-flex;align-items:center;min-height:2.75rem}
#config-editor textarea{height:clamp(16rem,45vh,32rem)}
.editor-buttons a{display:inline-flex;align-items:center;min-height:2.75rem;padding:.5rem}
table{min-width:0}
td,th{overflow-wrap:normal}
td:nth-child(-n+5),th{white-space:nowrap}
td:last-child,th:last-child{width:1%;white-space:nowrap}
td code{display:block;max-width:22rem;margin-top:.25rem;overflow-wrap:anywhere}
.editor-buttons{display:flex;flex-wrap:wrap;gap:.75rem}
td.num{text-align:right}
form.inline{display:inline-flex;flex-wrap:wrap;gap:.4rem;margin:.35rem .4rem .35rem 0;vertical-align:top}
button.small{font-size:.8rem;padding:.5rem .7rem}
.page-actions button.nav-link{background:none;border:0;border-radius:0;padding:0;min-height:2.75rem;color:var(--accent);font:inherit;text-decoration:underline;text-underline-offset:.2em;box-shadow:none}
.page-actions button.nav-link:hover{color:#c5d9fa;background:none}
.device-list-heading{display:flex;align-items:center;justify-content:space-between;gap:1rem;flex-wrap:wrap;margin:2rem 0 1rem;padding-top:1rem;border-top:1px solid var(--line)}
.device-list-heading h2{margin:0;padding:0;border:0}
.device-filter{display:flex;align-items:center;gap:.5rem;margin:0;font-size:.9rem}
.device-filter select{width:auto}
tr[hidden]{display:none}
.device-actions{display:flex;gap:.75rem;align-items:center;white-space:nowrap}
.device-actions form.inline{display:block;margin:0}
.device-toggle{display:inline-flex;align-items:center;justify-content:center;min-width:2.75rem;padding:.5rem;background:transparent!important;border-color:transparent!important;color:var(--text)!important}
.toggle-track{display:inline-block;width:2.4rem;height:1.4rem;border-radius:1rem;background:#465262;border:1px solid #798595;position:relative}
.toggle-track:after{content:"";position:absolute;top:.15rem;left:.15rem;width:1rem;height:1rem;border-radius:50%;background:#e0e6ef}
.device-toggle[aria-checked=true] .toggle-track{background:#30543d;border-color:#79a888}
.device-toggle[aria-checked=true] .toggle-track:after{left:1.15rem}
.device-menu summary{cursor:pointer;list-style:none;display:flex;align-items:center;justify-content:center;min-width:2.75rem;min-height:2.75rem;border:1px solid var(--line);border-radius:.4rem;font-size:1.4rem}
.device-menu summary::-webkit-details-marker{display:none}
.device-menu[open] summary{background:var(--input)}
.device-menu-panel{margin:0;inset:auto;width:max-content;max-width:calc(100vw - 16px);display:flex;gap:.4rem;position:fixed;z-index:1000;min-width:9rem;padding:.4rem;background:var(--panel);border:1px solid var(--line);border-radius:.5rem;box-shadow:0 .5rem 1.5rem #0008}
.device-menu-panel form+form{margin-top:0!important}
.device-menu-panel button{width:100%;min-height:44px}
.device-menu-panel form.inline{display:block;margin:0}
.device-menu-panel[hidden]{display:none!important}
button[value=stop],button[value=disable]{background:#302329;border-color:#79545d;color:#edbcc5}
button[value=stop]:hover,button[value=disable]:hover{background:#443039;border-color:#b4808d}
.device-menu:not([open]) .device-menu-panel{display:none}
.mobile-port{display:none}
@media(max-width:700px){
 body{padding:1.25rem 1rem calc(6rem + env(safe-area-inset-bottom) + var(--viewport-bottom,0px));min-width:0}
 h1{font-size:1.65rem}
 .page-actions{gap:.4rem 1rem}
 .page-actions a,.page-actions button.nav-link{display:inline-flex;align-items:center;min-height:44px}
 input,select,textarea{font-size:16px!important}
 button.small{min-height:44px;font-size:1rem}
 .device-list-heading{margin-top:1.25rem}
 .device-filter{width:100%;justify-content:space-between}
 .device-filter select{max-width:75%;flex:1}
 .table-scroll{overflow:visible;border:0;background:transparent}
 table,tbody{display:block;width:100%;min-width:0}
 table{border:0;background:transparent}
 tr:has(>th){position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%)}
 tr[data-device-enabled]{display:block;background:var(--panel);border:1px solid var(--line);border-radius:.65rem;margin-bottom:1rem;overflow:visible}
 tr[data-device-enabled][hidden]{display:none}
 tr[data-device-enabled] td{display:flex;align-items:center;justify-content:space-between;gap:.75rem;padding:.65rem .85rem;min-width:0;white-space:normal;border:0;overflow-wrap:anywhere;text-align:right}
 tr[data-device-enabled] td:before{content:attr(data-label);flex:0 0 5.5rem;text-align:left;font-size:.8rem;color:var(--muted)}
 tr[data-device-enabled] td:first-child{border-bottom:1px solid var(--line);font-weight:650;padding-top:.85rem;padding-bottom:.85rem}
 tr[data-device-enabled] td:last-child{border-top:1px solid var(--line);min-width:0;width:auto}
 tr[data-device-enabled] td.device-name:before{display:none}
 tr[data-device-enabled] td.device-name{text-align:left}
 .device-name>span:first-child{min-width:0;overflow-wrap:anywhere}
 .mobile-port{display:block;flex-shrink:0;color:var(--muted);font-weight:400;font-size:.85rem}
 tr[data-device-enabled] td.device-port{display:none}
 .readiness-label{white-space:normal;text-align:right;max-width:100%;font-size:.85rem}
 .device-actions{justify-content:flex-end;gap:1rem}
 .device-menu-panel{right:auto;bottom:auto;min-width:0;display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:.35rem;padding:.65rem;box-shadow:0 -.5rem 2rem #0009}
 .device-menu-panel{height:calc(44px + 1.3rem + 2px);max-height:calc(100dvh - 16px);grid-template-rows:44px;grid-auto-rows:44px;align-items:start;align-content:start;overflow:auto}
 .device-menu-panel>form.inline{height:44px;min-height:0;align-self:start}
 .device-menu-panel button{padding:.5rem .2rem;min-width:0;height:44px;max-height:44px;align-self:start}
 /* One compact mobile row, retaining the desktop table markup. */
 tr[data-device-enabled]{display:grid;grid-template-columns:2.75rem minmax(0,1fr) auto minmax(0,1fr) 2.75rem;gap:.3rem;align-items:center;padding:.5rem}
 tr[data-device-enabled] td.device-name{display:contents}
 .device-name>span:first-child{grid-column:2;grid-row:1;font-size:.8rem}
 .mobile-port{grid-column:3;grid-row:1;font-size:.75rem}
 tr[data-device-enabled] td[data-readiness],tr[data-device-enabled] td[data-label="Driver State"]{grid-column:4;grid-row:1;display:block;padding:0;text-align:left}
 tr[data-device-enabled] td:before{display:none}
 tr[data-device-enabled] td:last-child,.device-actions{display:contents;border:0}
 .device-actions>form{grid-column:1;grid-row:1}
 .device-actions>.device-menu{grid-column:5;grid-row:1}
 .device-toggle{padding:0;min-height:44px}
 .readiness-label{padding:.2rem .3rem;font-size:.7rem;text-align:left;line-height:1.3}
 .device-menu summary{min-width:44px;min-height:44px}
 .editor-page>form{padding:.85rem}
 .editor-buttons{gap:.5rem}
}
</style></head>
<body{{if or .EditPage .AddPage}} class="editor-page"{{end}}>
{{if .EditPage}}
<nav aria-label="Back"><a href="/setup">← Back to devices</a></nav>
<h1>{{if .EditInstance}}Edit {{.EditInstance}}{{else}}Edit configuration{{end}}</h1>
<p class="sub">Check your changes, then save to return to the device list.</p>
{{with .Banner}}<div class="banner {{$.BannerKind}}" role="alert">{{.}}</div>{{end}}
{{if .EditInstance}}
<form id="config-editor" method="post" action="/setup/edit">
<input type="hidden" name="instance" value="{{.EditInstance}}">
<p class="sub"><code>{{.EditPath}}</code></p>
<textarea name="text" rows="18" aria-label="Device configuration">{{.EditText}}</textarea>
<div id="syntax-status" class="banner" role="status" aria-live="polite">Checking JSON syntax…</div>
<div id="config-status" class="banner" role="status" aria-live="polite">Configuration has not been checked.</div>
<p class="editor-buttons"><button id="check-config" type="button" disabled>Check configuration</button>
<button id="save-config" type="submit" disabled>Save</button>
<a href="/setup">Cancel</a></p>
<noscript><p>Enable JavaScript to check this draft and save it.</p></noscript>
<p>
<span class="help">The file is the admin file, JSON with <code>//</code> and <code>/* */</code> comments allowed; it is checked before it is written. A running device picks the change up on restart; a disabled one when enabled.</span></p>
</form>
<script>{{.EditorJS}}</script>
{{end}}
{{else if .AddPage}}
<nav aria-label="Back"><a href="/setup">← Back to devices</a></nav>
<h1>Add a device</h1>
<p class="sub">Choose a compiled-in or installed driver. Create a disabled device, then configure it on the next page.</p>
{{with .Banner}}<div class="banner {{$.BannerKind}}" role="alert">{{.}}</div>{{end}}
<form method="post" action="/setup/add">
<label><span class="lab">Driver</span><select name="driver">{{range .Drivers}}<option value="{{.}}"{{if eq . $.AddDriver}} selected{{end}}>{{.}}</option>{{end}}</select></label>
<label><span class="lab">Instance name</span><input type="text" name="instance" placeholder="main-camera" value="{{.AddInstance}}"></label>
<p class="editor-buttons"><button type="submit">Create and configure</button><a href="/setup">Cancel</a></p>
<span class="help">Writes <code>devices.d/&lt;instance&gt;.json</code> with every key commented at its default and enable false. The next page opens its configuration editor. Enable it from the device list when ready.</span>
</form>
{{else}}
<h1>alpacahurd</h1>
<nav class="page-actions" aria-label="Management">
<a href="/setup/logs" target="_blank" rel="noopener">Logs ↗</a>
<a href="/setup/check">Check configuration</a>
<a href="/setup/add">Add device</a>
<button type="button" id="refresh-readiness" class="nav-link">Refresh status</button>
</nav>
<p class="sub">{{.Version}} · listening on port {{.Port}} · up {{.Uptime}}<br>config <code>{{.ConfigDir}}</code><br>state <code>{{.StateDir}}</code><br>supervisor {{.Supervisor}}</p>
{{with .Banner}}<div class="banner {{$.BannerKind}}">{{.}}</div>{{end}}
{{with .CheckOut}}<h2>Configuration check</h2><pre class="result">{{.}}</pre>{{end}}
<div class="device-list-heading"><h2>Devices</h2><label class="device-filter" for="device-state-filter">Show <select id="device-state-filter"><option value="all">All states</option><option value="enabled">Enabled only</option><option value="disabled">Disabled only</option></select></label></div>
<div class="table-scroll" role="region" aria-label="Devices" tabindex="0">
<table>
<tr><th>Name</th><th>Port</th><th>Driver State</th><th>Enabled</th></tr>
{{range .Rows}}<tr data-device-enabled="{{.Enabled}}">
<td data-label="Name" class="device-name"><span>{{if and .Enabled .Running .Setup}}<a href="{{.Setup}}" title="Open device setup">{{.Name}}</a>{{else}}{{.Name}}{{end}}</span><span class="mobile-port">{{if .Port}}{{.Port}}{{else}}Automatic{{end}}</span></td>
<td data-label="Port" class="device-port">{{if .Port}}{{.Port}}{{else}}Automatic{{end}}</td>
<td data-label="Driver State" {{if .Edit}}data-readiness="{{.Instance}}"{{end}}><span class="readiness-label neutral">{{if .Enabled}}Enabled{{else}}Disabled{{end}} · Not verified</span></td>
<td data-label="Enabled"><div class="device-actions">
<form class="inline" method="post" action="/setup"><input type="hidden" name="instance" value="{{.Instance}}"><button class="device-toggle" type="submit" role="switch" aria-checked="{{.Enabled}}" aria-label="Enable {{.Instance}}" name="action" value="{{if .Enabled}}disable{{else}}enable{{end}}" {{if not (or .Actions .Toggle)}}disabled{{end}} title="{{if .Enabled}}Disable and stop this device{{else}}Enable and start this device{{end}}"><span class="toggle-track" aria-hidden="true"></span></button></form>
<details class="device-menu"><summary aria-label="More actions for {{.Instance}}" title="More actions">⋯</summary><div class="device-menu-panel">{{if and .Enabled .Running .Setup}}<form class="inline" method="get" action="{{.Setup}}"><button class="small" type="submit">Setup</button></form>{{else}}<button class="small" type="button" disabled title="Setup is available when the device is enabled and running">Setup</button>{{end}}{{if .Edit}}<form class="inline" method="get" action="/setup/edit"><input type="hidden" name="instance" value="{{.Instance}}"><button class="small" type="submit">Edit</button></form>{{else}}<button class="small" type="button" disabled>Edit</button>{{end}}<form class="inline" method="post" action="/setup"><input type="hidden" name="instance" value="{{.Instance}}"><button class="small" name="action" value="restart" {{if not (and .Enabled .Running (or .Actions (and .Toggle .Reload)))}}disabled{{end}} title="Restart the driver and reread its configuration">Restart</button></form><form class="inline" method="post" action="/setup"><input type="hidden" name="instance" value="{{.Instance}}"><button class="small" name="action" value="delete" {{if or (not .Edit) .Enabled .Running}}disabled{{end}} title="Delete this device configuration; disable and stop it first">Delete</button></form></div></details>
</div></td>
</tr>{{end}}
</table>
</div>
{{end}}
<script>{{.ReadinessJS}}</script>
</body></html>`))
