package hurd

import (
	"context"
	"encoding/json"
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

	"github.com/mikefsq/goalpaca/devicemain"
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// The orchestrator page, at /setup/hurd on every server the hurd runs, is the
// one place that sees the whole herd: every configured device with how it runs
// (in process or as a separate binary), whether it is up, its port and device
// number, a link to its own setup page, and the actions the supervisor offers.
// It also runs the equivalent of -check on demand and adds a device by writing
// a commented device file from a driver's schema.
//
// Everything on it comes from the config files, the resolution, and the
// Supervisor interface, so it renders the same in both layouts: with the
// no-op supervisor every device is in process and the actions refuse politely.

// orchestrator is the shared state behind the page: what serve built, plus the
// supervisor and the config paths, read-locked by the page.
type orchestrator struct {
	cfgPath   string
	cfg       *Config
	sup       Supervisor
	mu        sync.RWMutex
	rows      []orchRow
	servers   map[int]*alpacadev.Server // by bound port
	startedAt time.Time
	page      int // the port the orchestrator page bound; 0 until it does
	// extra holds registrations that match no configured instance, by
	// UniqueID (else address:port); see noteRegistration.
	extra map[string]*alpacadev.Registration

	// What enabling a device at runtime needs from serve: the context the
	// servers run under, the listen addresses and request logger for a new
	// server, the servers by key with their device numbers, how many scanning
	// servers exist (each gets its own port window), and the discovery
	// responder to tell about a new port. Set by serve; nil in a test that
	// renders only.
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
	// reg is the last heartbeat from a separate binary running this entry,
	// matched by instance; nil until one arrives. It carries the bound port
	// and the address the device is reachable at.
	reg *alpacadev.Registration
	// reloadable is set for an in-process device the server can reload in
	// place; a separate binary is asked over HTTP and decides for itself.
	reloadable bool
}

// noteRegistration records a heartbeat against the row whose instance it
// names. A registration naming no known instance is a device someone runs by
// hand in register mode; it is listed too, so the page shows every device the
// orchestrator answers discovery for.
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

// registeredState is the page's word for a heartbeat: where the device is and
// how fresh the registration is; empty when the registration has expired.
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

// reload reloads one device in place: an in-process device through its
// server, a separate binary through the reload form on its own setup page,
// reached at the address and port its registration carries. inst names a
// configured entry; uniqueID names a registered device outside the config.
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

// setupPages returns the sub-pages the hurd mounts on its own setup-port
// server beside the page itself at /setup: /setup/check and /setup/add.
func (o *orchestrator) setupPages() map[string]http.Handler {
	return map[string]http.Handler{"check": o, "add": o, "edit": o}
}

// redirectPages returns what a device server mounts: /setup/hurd redirects to
// the orchestrator page on the setup port, using the host the browser addressed
// so the link works from any machine.
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
	case rest == "" || rest == "/":
		if r.Method == http.MethodPost {
			o.handleAction(w, r)
			return
		}
		o.render(w, r, "", "")
	case rest == "/check":
		var b strings.Builder
		errs := checkConfig(&b, o.cfg)
		kind := "ok"
		if errs > 0 {
			kind = "error"
		}
		o.render(w, r, b.String(), kind)
	case rest == "/add":
		if r.Method == http.MethodPost {
			o.handleAdd(w, r)
			return
		}
		o.render(w, r, "", "")
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
	case "reload":
		err = o.reload(ctx, inst, r.PostForm.Get("uniqueid"))
	case "logs":
		var b strings.Builder
		if err := o.sup.Logs(ctx, inst, 100, &b); err != nil {
			o.render(w, r, fmt.Sprintf("logs %s: %v", inst, err), "error")
			return
		}
		o.renderOutput(w, r, b.String())
		return
	case "start":
		err = o.sup.Start(ctx, inst)
	case "stop":
		err = o.sup.Stop(ctx, inst)
	case "restart":
		err = o.sup.Restart(ctx, inst)
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

// handleAdd writes a new device file from a driver's commented schema. The
// file is disabled and every key but driver is commented, so adding a device
// changes nothing until the admin edits it; the page says where it went.
func (o *orchestrator) handleAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		o.render(w, r, "could not read the form", "error")
		return
	}
	driver, inst := r.PostForm.Get("driver"), strings.TrimSpace(r.PostForm.Get("instance"))
	drv, ok := registry.Lookup(driver)
	if !ok {
		o.render(w, r, fmt.Sprintf("driver %q is not compiled in", driver), "error")
		return
	}
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
	var b strings.Builder
	if err := devicemain.WriteCommentedDeviceFile(&b, drv, examplePortBase); err != nil {
		o.render(w, r, err.Error(), "error")
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		o.render(w, r, err.Error(), "error")
		return
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		o.render(w, r, fmt.Sprintf("write %s: %v", path, err), "error")
		return
	}
	// The new entry joins the table at once, disabled, so it can be enabled
	// from there without a restart.
	if spec, err := loadDeviceFile(path, stateDevicesDir()); err == nil {
		o.mu.Lock()
		o.rows = append(o.rows, orchRow{spec: spec, res: resolveDriver(spec), port: spec.Port})
		o.cfg.Devices = append(o.cfg.Devices, spec)
		o.mu.Unlock()
	}
	o.render(w, r, fmt.Sprintf("wrote %s: uncomment the keys it needs, then enable it in the table above", path), "ok")
}

// handleEdit serves the raw device file editor: GET shows the admin file of
// the named devices.d entry; POST validates the text as a device file (JSON
// with comments, naming a driver) and writes it in place, then re-reads the
// entry into its row so the table and the next enable or reload see it. The
// instance has to be one the table knows, which rules out any other path.
func (o *orchestrator) handleEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		o.render(w, r, "", "")
		return
	}
	if err := r.ParseForm(); err != nil {
		o.render(w, r, "could not read the form", "error")
		return
	}
	inst, text := r.PostForm.Get("instance"), r.PostForm.Get("text")
	o.mu.Lock()
	var row *orchRow
	for i := range o.rows {
		if o.rows[i].spec.Instance == inst && o.rows[i].spec.Source != "" {
			row = &o.rows[i]
			break
		}
	}
	if row == nil {
		o.mu.Unlock()
		o.render(w, r, fmt.Sprintf("no device file for %q", inst), "error")
		return
	}
	path := row.spec.Source
	var m map[string]json.RawMessage
	if err := json.Unmarshal(devicemain.StripComments([]byte(text)), &m); err != nil {
		o.mu.Unlock()
		o.render(w, r, fmt.Sprintf("%s not saved: not valid JSON: %v", inst, err), "error")
		return
	}
	if _, ok := m["driver"]; !ok {
		o.mu.Unlock()
		o.render(w, r, fmt.Sprintf("%s not saved: the file names no \"driver\"", inst), "error")
		return
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if err := writeFileAtomic(path, []byte(text)); err != nil {
		o.mu.Unlock()
		o.render(w, r, fmt.Sprintf("%s not saved: %v", inst, err), "error")
		return
	}
	msg := fmt.Sprintf("saved %s", path)
	if spec, err := loadDeviceFile(path, stateDevicesDir()); err == nil {
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
		if row.inProcess {
			msg += "; reload the device to apply it"
		} else if spec.enabled() {
			msg += "; enable the device to start it"
		}
	} else {
		msg += fmt.Sprintf("; it does not load as a device file: %v", err)
	}
	o.mu.Unlock()
	o.render(w, r, msg, "ok")
}

// writeFileAtomic writes b to path through a temp file and a rename, so a
// crash mid-write leaves the old file.
func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// pageRow is one rendered row.
type pageRow struct {
	Instance, Driver, Type, Name, How, Port, State, Setup, Argv, Skipped string
	Num                                                                  int
	Enabled, Running, Actions                                            bool
	// Reload offers the reload button: an in-process device with a reloader,
	// or a running separate binary, which is asked over HTTP. UniqueID names
	// a registered device that matches no configured instance.
	Reload   bool
	UniqueID string
	// Toggle offers enable or disable through the orchestrator (setEnabled)
	// for a devices.d entry that is in process or could be: the supervisor
	// form carries the same buttons for a separate binary.
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
	// The device file editor, shown when EditInstance is set: the admin file
	// of one devices.d entry, as text, with its path.
	EditInstance string
	EditPath     string
	EditText     string
}

func (o *orchestrator) render(w http.ResponseWriter, r *http.Request, banner, kind string) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	view := pageView{
		ConfigDir:  filepath.Dir(o.cfgPath),
		StateDir:   stateDirRoot(),
		Supervisor: o.sup.Name(),
		Version:    version,
		Port:       o.page,
		Uptime:     time.Since(o.startedAt).Round(time.Second).String(),
	}
	if strings.HasSuffix(r.URL.Path, "/check") || kind == "output" {
		view.CheckOut, view.CheckKind = banner, kind
	} else {
		view.Banner, view.BannerKind = banner, kind
	}
	if inst := r.URL.Query().Get("instance"); inst != "" && strings.HasSuffix(r.URL.Path, "/edit") {
		for _, row := range o.rows {
			if row.spec.Instance == inst && row.spec.Source != "" {
				view.EditInstance, view.EditPath = inst, row.spec.Source
				if b, err := os.ReadFile(row.spec.Source); err == nil {
					view.EditText = string(b)
				} else {
					view.EditText = "// " + err.Error()
				}
			}
		}
	}
	// The CSS comes from whichever server is rendering, so the page matches.
	// The CSS is goalpaca's default setup stylesheet, the same every device
	// page here uses, so the page matches whether or not any device server
	// exists yet.
	view.CSS = template.CSS(alpacadev.DefaultSetupTemplates().CSS)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	for _, row := range o.rows {
		pr := pageRow{
			Instance: row.spec.Instance, Driver: row.spec.Driver, Type: string(row.devType),
			Name: row.deviceName, Num: row.num, Skipped: row.skipped,
			Enabled: row.spec.enabled(),
		}
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
			// A disabled entry: its file exists and its driver resolves (or
			// not); nothing runs. The template says "disabled". A devices.d
			// entry offers enable here; a separate binary through its
			// supervisor form.
			pr.How = row.res.kind.String()
			if row.port != 0 {
				pr.Port = fmt.Sprint(row.port)
			}
			pr.Toggle = row.spec.Instance != "" && row.res.kind == compiledIn
			pr.Actions = row.spec.Instance != "" && row.res.kind == installedBinary
		case regState != "":
			// A separate binary is running and heartbeating; the registration
			// says where and on which port, whatever the supervisor knows.
			pr.How, pr.State, pr.Running = "separate binary", regState, true
			pr.Port = fmt.Sprint(row.reg.AlpacaPort)
			host := hostOf(r)
			if !row.reg.Local {
				host = row.reg.Addr.String()
			}
			pr.Setup = fmt.Sprintf("http://%s:%d/setup", host, row.reg.AlpacaPort)
			pr.Actions = row.skipped == ""
			pr.Reload = true
		case row.skipped != "":
			// Enabled but not running: a driver that resolved nowhere, or a
			// build that failed. The switch stays available so the entry can
			// be turned off (or tried again once its file is fixed).
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
			pr.How = "separate binary"
			pr.Actions = true
			if row.port != 0 {
				pr.Port = fmt.Sprint(row.port)
				pr.Setup = fmt.Sprintf("http://%s:%d/setup/v1/%s/%d/setup", hostOf(r), row.port, row.devType, row.num)
			}
			st, err := o.sup.Status(ctx, row.spec.Instance)
			if err != nil {
				pr.State = "status: " + err.Error()
			} else {
				pr.State, pr.Running = st.State, st.Running
				if !st.Installed {
					pr.State = "not installed with " + o.sup.Name()
				}
			}
		}
		view.Rows = append(view.Rows, pr)
	}
	// Devices registered by hand, outside the config.
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
			Instance: inst, Type: e.DeviceType, Name: e.DeviceName,
			How: "registered", State: state, Running: true, Port: fmt.Sprint(e.AlpacaPort),
			Setup:  fmt.Sprintf("http://%s:%d/setup", host, e.AlpacaPort),
			Reload: true, UniqueID: e.UniqueID,
		})
	}
	for _, d := range registry.All() {
		view.Drivers = append(view.Drivers, d.Name)
	}
	sort.Strings(view.Drivers)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = orchTmpl.Execute(w, view)
}

// renderOutput renders the page with text (a log tail) in the output pane.
func (o *orchestrator) renderOutput(w http.ResponseWriter, r *http.Request, text string) {
	o.render(w, r, text, "output")
}

// hostOf returns the host the browser addressed, without its port, so links to
// other ports on this machine use the same address the user reached us by.
func hostOf(r *http.Request) string {
	h := r.Host
	if u, err := url.Parse("http://" + h); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return h
}

var orchTmpl = template.Must(template.New("hurd").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>alpacahurd</title><style>{{.CSS}}
td.num{text-align:right}
form.inline{display:inline}
button.small{font-size:.85rem;padding:.2rem .6rem;margin:0 .1rem}
</style></head>
<body>
<h1>alpacahurd</h1>
<p class="sub">{{.Version}} · listening on port {{.Port}} · up {{.Uptime}}<br>config <code>{{.ConfigDir}}</code><br>state <code>{{.StateDir}}</code><br>supervisor {{.Supervisor}}</p>
{{with .Banner}}<div class="banner {{$.BannerKind}}">{{.}}</div>{{end}}
<h2>Devices</h2>
<table>
<tr><th>Instance</th><th>Driver</th><th>Type</th><th class="num">#</th><th>Name</th><th>How</th><th>Port</th><th>State</th><th></th></tr>
{{range .Rows}}<tr>
<td>{{.Instance}}</td><td>{{.Driver}}</td><td>{{.Type}}</td><td class="num">{{.Num}}</td><td>{{.Name}}</td>
<td>{{.How}}{{with .Argv}}<br><code>{{.}}</code>{{end}}</td>
<td>{{.Port}}</td>
<td>{{.State}}{{if not .Enabled}}{{if .State}} · {{end}}disabled{{end}}{{with .Skipped}}<br>{{.}}{{end}}</td>
<td>{{with .Setup}}<a href="{{.}}">setup</a>{{end}}{{if .Edit}} <a href="/setup/edit?instance={{.Instance}}">edit</a>{{end}}
{{if .Actions}}
<form class="inline" method="post" action="/setup"><input type="hidden" name="instance" value="{{.Instance}}">
{{if .Running}}<button class="small" name="action" value="stop">stop</button><button class="small" name="action" value="restart">restart</button>{{else}}<button class="small" name="action" value="start">start</button>{{end}}
{{if .Enabled}}<button class="small" name="action" value="disable">disable</button>{{else}}<button class="small" name="action" value="enable">enable</button>{{end}}
<button class="small" name="action" value="logs">logs</button>
</form>{{end}}
{{if .Toggle}}<form class="inline" method="post" action="/setup"><input type="hidden" name="instance" value="{{.Instance}}">{{if .Enabled}}<button class="small" name="action" value="disable" title="close the hardware and remove the device; the switch is recorded in its state file">disable</button>{{else}}<button class="small" name="action" value="enable" title="construct the device and serve it now; the switch is recorded in its state file">enable</button>{{end}}</form>{{end}}
{{if .Reload}}<form class="inline" method="post" action="/setup"><input type="hidden" name="instance" value="{{.Instance}}"><input type="hidden" name="uniqueid" value="{{.UniqueID}}"><button class="small" name="action" value="reload" title="re-read the device's configuration and reopen its hardware; the port stays">reload</button></form>{{end}}</td>
</tr>{{end}}
</table>
<h2>Check</h2>
<p><a href="/setup/check">Run the config check</a> (what <code>alpacahurd -check</code> prints).</p>
{{with .CheckOut}}<pre class="result">{{.}}</pre>{{end}}
{{if .EditInstance}}
<h2>Edit {{.EditInstance}}</h2>
<form method="post" action="/setup/edit">
<input type="hidden" name="instance" value="{{.EditInstance}}">
<p class="sub"><code>{{.EditPath}}</code></p>
<textarea name="text" rows="24" style="width:100%;font-family:ui-monospace,monospace">{{.EditText}}</textarea>
<p><button type="submit">Save</button>
<span class="help">The file is the admin file, JSON with <code>//</code> and <code>/* */</code> comments allowed; it is checked before it is written. A running device picks the change up on reload; a disabled one when enabled.</span></p>
</form>
{{end}}
<h2>Add a device</h2>
<form method="post" action="/setup/add">
<label><span class="lab">Driver</span><select name="driver">{{range .Drivers}}<option>{{.}}</option>{{end}}</select></label>
<label><span class="lab">Instance name</span><input type="text" name="instance" placeholder="main-camera"></label>
<button type="submit">Write device file</button>
<span class="help">Writes <code>devices.d/&lt;instance&gt;.json</code> with every key commented at its default and enable false. Edit it, then enable it in the table above.</span>
</form>
</body></html>`))
