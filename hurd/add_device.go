package hurd

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mikefsq/goalpaca/devicemain"
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

//go:embed add_device.js
var addScript string

// maxAddDrafts bounds the pending device selections. The page starts a selection on every
// driver change, so a full table evicts the oldest instead of refusing.
const maxAddDrafts = 32

// legacyMultiKeys names the multi-device array of installed drivers built before goalpaca's
// -schema json reported multiKey. Compiled-in drivers and newer binaries describe their own.
var legacyMultiKeys = map[string]string{"astrocam": "cameras"}

type addField struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Default     string   `json:"default"`
	Help        string   `json:"help"`
	Constraints string   `json:"constraints"`
	Options     []string `json:"options"`
}
type addFound struct {
	Label      string                     `json:"label"`
	Values     map[string]json.RawMessage `json:"values"`
	Configured string                     `json:"configured,omitempty"`
}
type addDiscovery struct {
	Driver    string     `json:"driver"`
	Supported bool       `json:"supported"`
	Identity  []string   `json:"identity"`
	Devices   []addFound `json:"devices"`
	Error     string     `json:"error,omitempty"`
}
type addDraft struct {
	Token     string                     `json:"token"`
	Fields    []addField                 `json:"fields"`
	Discovery addDiscovery               `json:"discovery"`
	Prototype string                     `json:"prototype"`
	Values    map[string]json.RawMessage `json:"values"`
	// Warnings are problems that did not stop the selection, such as an unreadable devices.d
	// file whose device could not be checked for duplicates.
	Warnings []string `json:"warnings,omitempty"`
	original DeviceSpec
	text     string
	template string // the driver's fresh template, before any prototype replaced text
	multiKey string // the driver's multi-device array, e.g. astrocam's "cameras"
	expires  time.Time
}

func addReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func addError(w http.ResponseWriter, err error) {
	addReply(w, 422, map[string]string{"error": err.Error()})
}

// Only executables from the installation manifest may supply browser metadata.
func addCommand(ctx context.Context, exe string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.WaitDelay = time.Second
	var out, diagnostics checkOutput
	cmd.Stdout, cmd.Stderr = &out, &diagnostics
	err := cmd.Run()
	if err != nil {
		return []byte(out.String()), fmt.Errorf("%s: %w: %s", filepath.Base(exe), err, strings.TrimSpace(diagnostics.String()))
	}
	return []byte(out.String()), nil
}

func (o *orchestrator) addMetadata(ctx context.Context, driver string) (*addDraft, error) {
	text, err := newDeviceTemplate(ctx, o.cfgPath, driver)
	if err != nil {
		return nil, err
	}
	d := &addDraft{text: text, template: text, expires: time.Now().Add(10 * time.Minute)}
	clean, err := editorJSON(text)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(clean, &d.Values); err != nil {
		return nil, err
	}
	var canonical string
	_ = json.Unmarshal(d.Values["driver"], &canonical)
	if canonical == "" {
		return nil, fmt.Errorf("driver template names no driver")
	}

	if drv, ok := registry.Lookup(driver); ok {
		d.multiKey = drv.MultiKey
		if drv.Config != nil {
			fields, err := alpacadev.FieldsFromStruct(drv.Config(), nil, "")
			if err != nil {
				return nil, err
			}
			for _, f := range fields {
				d.Fields = append(d.Fields, addField{f.Name, f.Label, f.Type, f.Value, f.Help, f.Constraints, f.Options})
			}
		}
		var out bytes.Buffer
		err = devicemain.Discover(ctx, drv, &out)
		if e := json.Unmarshal(out.Bytes(), &d.Discovery); e != nil {
			return nil, e
		}
		if err != nil && d.Discovery.Error == "" {
			d.Discovery.Error = err.Error()
		}
	} else if err := o.addInstalledMetadata(ctx, driver, canonical, d); err != nil {
		return nil, err
	}

	if d.Discovery.Driver != canonical {
		return nil, fmt.Errorf("driver template and discovery identity differ")
	}
	// Go encodes nil slices and maps as null; the page expects arrays and objects.
	if d.Discovery.Identity == nil {
		d.Discovery.Identity = []string{}
	}
	if d.Discovery.Devices == nil {
		d.Discovery.Devices = []addFound{}
	}
	for i := range d.Discovery.Devices {
		if d.Discovery.Devices[i].Values == nil {
			d.Discovery.Devices[i].Values = map[string]json.RawMessage{}
		}
	}
	if d.Fields == nil {
		d.Fields = []addField{}
	}
	return d, nil
}

// addInstalledMetadata asks an installed driver for its fields and devices. A binary without
// -schema json or -discover, from an older package, still gets the template-only path: the
// page shows no device list and the driver's template supplies the settings.
func (o *orchestrator) addInstalledMetadata(ctx context.Context, driver, canonical string, d *addDraft) error {
	installed, err := installedDrivers(o.cfgPath)
	if err != nil {
		return err
	}
	exe := installed[driver].Executable

	var schema struct {
		Driver   string     `json:"driver"`
		MultiKey string     `json:"multiKey"`
		Fields   []addField `json:"fields"`
	}
	raw, schemaErr := addCommand(ctx, exe, "-schema", "json")
	if schemaErr == nil {
		if err := json.Unmarshal(raw, &schema); err != nil {
			return fmt.Errorf("invalid driver schema: %w", err)
		}
		if schema.Driver != canonical {
			return fmt.Errorf("driver schema and template identity differ")
		}
		d.Fields = schema.Fields
	} else {
		d.Warnings = append(d.Warnings, "this driver package cannot describe its settings; review them in the configuration file after adding")
	}
	d.multiKey = schema.MultiKey
	if d.multiKey == "" {
		d.multiKey = legacyMultiKeys[canonical]
	}

	raw, scanErr := addCommand(ctx, exe, "-discover")
	if err := json.Unmarshal(raw, &d.Discovery); err != nil {
		reason := scanErr
		if reason == nil {
			reason = err
		}
		d.Discovery = addDiscovery{Driver: canonical,
			Error: fmt.Sprintf("automatic discovery is unavailable from this driver package: %v", reason)}
		return nil
	}
	if scanErr != nil && d.Discovery.Error == "" {
		d.Discovery.Error = scanErr.Error()
	}
	return nil
}

func rawValue(m map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}
func identityValue(m map[string]json.RawMessage, keys []string) (string, json.RawMessage) {
	for _, key := range keys {
		v, ok := rawValue(m, key)
		if !ok || string(v) == "null" {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) == "" {
			continue
		}
		return key, v
	}
	return "", nil
}
func sameValue(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return fmt.Sprint(x) == fmt.Sprint(y)
}

// Include multi-device blocks so an existing camera array cannot be assigned again.
func deviceIdentityMaps(raw json.RawMessage) []map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	result := []map[string]json.RawMessage{m}
	for _, v := range m {
		var blocks []json.RawMessage
		if json.Unmarshal(v, &blocks) == nil {
			for _, block := range blocks {
				result = append(result, deviceIdentityMaps(block)...)
			}
		}
	}
	return result
}

// Match the strongest identity already configured, even for disabled entries.
func configuredDevice(specs []DeviceSpec, discovery addDiscovery, values map[string]json.RawMessage) string {
	for _, spec := range specs {
		if spec.Driver != discovery.Driver {
			continue
		}
		for _, m := range deviceIdentityMaps(spec.Raw) {
			key, v := identityValue(m, discovery.Identity)
			candidate, ok := rawValue(values, key)
			if key != "" && ok && sameValue(v, candidate) {
				if spec.Instance != "" {
					return spec.Instance
				}
				return "(inline)"
			}
		}
	}
	return ""
}

// addSpecs lists every configured device for duplicate checks. An unreadable devices.d file
// does not block adding: it is skipped and named in a warning, since its device could not be
// checked.
func (o *orchestrator) addSpecs() ([]DeviceSpec, []string, error) {
	names, err := filepath.Glob(filepath.Join(devicesDirFor(o.cfgPath), "*.json"))
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(names)
	var specs []DeviceSpec
	var warnings []string
	for _, path := range names {
		spec, err := loadDeviceFile(path, stateDevicesDir())
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skipped %s, which could not be read, so its device was not checked for duplicates: %v", filepath.Base(path), err))
			continue
		}
		specs = append(specs, spec)
	}
	for _, spec := range o.cfg.Devices {
		if spec.Source == "" {
			specs = append(specs, spec)
		}
	}
	return specs, warnings, nil
}

// untouchedTemplate reports whether a device file still holds the driver's template, ignoring
// the enable flag and port that seeding and earlier adds set. Only such a file may be reused as
// a prototype: a disabled file the user edited is a real configuration even without an
// identity key, because a driver can select its first device by default.
func untouchedTemplate(fileText, templateText string) bool {
	parse := func(text string) map[string]any {
		clean, err := editorJSON(text)
		if err != nil {
			return nil
		}
		var m map[string]any
		if json.Unmarshal(clean, &m) != nil {
			return nil
		}
		for k := range m {
			if strings.EqualFold(k, "enable") || strings.EqualFold(k, "port") {
				delete(m, k)
			}
		}
		return m
	}
	a, b := parse(fileText), parse(templateText)
	return a != nil && b != nil && reflect.DeepEqual(a, b)
}

func (o *orchestrator) handleAddOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Use POST", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := r.ParseForm(); err != nil {
		addError(w, err)
		return
	}
	d, err := o.addMetadata(r.Context(), r.PostForm.Get("driver"))
	if err != nil {
		addError(w, err)
		return
	}
	if err := o.registerAddDraft(d); err != nil {
		addError(w, err)
		return
	}
	addReply(w, 200, d) // written after the lock is released
}

// registerAddDraft picks a prototype, marks devices already configured and stores the draft.
func (o *orchestrator) registerAddDraft(d *addDraft) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	specs, warnings, err := o.addSpecs()
	if err != nil {
		return err
	}
	d.Warnings = append(d.Warnings, warnings...)
	// Reuse only disabled, unassigned files that still hold the driver's template.
	for _, spec := range specs {
		if spec.Source == "" || spec.Driver != d.Discovery.Driver || spec.enabled() {
			continue
		}
		data, err := os.ReadFile(spec.Source)
		if err != nil || !untouchedTemplate(string(data), d.template) {
			continue
		}
		assigned := false
		for _, block := range deviceIdentityMaps(spec.Raw) {
			if key, _ := identityValue(block, d.Discovery.Identity); key != "" {
				assigned = true
			}
		}
		if assigned {
			continue
		}
		var values map[string]json.RawMessage
		if json.Unmarshal(spec.Raw, &values) != nil {
			continue
		}
		// Arrays other than the driver's own multi-device key stay in the advanced editor.
		hasBlocks := false
		for key, value := range values {
			var blocks []json.RawMessage
			if json.Unmarshal(value, &blocks) == nil && len(blocks) > 0 && !strings.EqualFold(key, d.multiKey) {
				hasBlocks = true
			}
		}
		if hasBlocks {
			continue
		}
		d.original, d.text, d.Prototype, d.Values = spec, string(data), spec.Instance, values
		break
	}
	// A multi-device template shows its first block's settings as the device's own.
	if d.multiKey != "" {
		var blocks []map[string]json.RawMessage
		if raw, ok := rawValue(d.Values, d.multiKey); ok && json.Unmarshal(raw, &blocks) == nil && len(blocks) > 0 {
			for key, value := range blocks[0] {
				if !isCommonKey(key) || key == "name" {
					d.Values[key] = value
				}
			}
		}
	}
	for i := range d.Discovery.Devices {
		d.Discovery.Devices[i].Configured = configuredDevice(specs, d.Discovery, d.Discovery.Devices[i].Values)
	}

	if o.addDrafts == nil {
		o.addDrafts = map[string]*addDraft{}
	}
	now := time.Now()
	for token, draft := range o.addDrafts {
		if now.After(draft.expires) {
			delete(o.addDrafts, token)
		}
	}
	for len(o.addDrafts) >= maxAddDrafts {
		oldest := ""
		for token, draft := range o.addDrafts {
			if oldest == "" || draft.expires.Before(o.addDrafts[oldest].expires) {
				oldest = token
			}
		}
		delete(o.addDrafts, oldest)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	d.Token = hex.EncodeToString(random[:])
	o.addDrafts[d.Token] = d
	return nil
}

// usedPorts lists the ports configured devices, running rows and the setup page hold. Called
// with o.mu held.
func (o *orchestrator) usedPorts(specs []DeviceSpec) map[int]bool {
	used := map[int]bool{o.page: true}
	for _, s := range specs {
		used[s.Port] = true
	}
	for _, row := range o.rows {
		used[row.port] = true
	}
	return used
}

// availableAddPort finds a free port outside used. It probes with net.Listen, so call it
// without o.mu held.
func availableAddPort(used map[int]bool) (int, error) {
	for port := portScanBase; port <= 65535; port++ {
		if used[port] {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue
		}
		listener.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no available Alpaca port")
}

func (o *orchestrator) handleAddDevice(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		addError(w, err)
		return
	}
	inst, on, err := o.saveAddDevice(r.Context(), r.PostForm)
	if err != nil {
		addError(w, err)
		return
	}
	message := "Device configured; it remains disabled"
	if on {
		ctx, cancel := context.WithTimeout(r.Context(), enableTimeout)
		defer cancel()
		message, err = o.enableOrRollback(ctx, inst)
		if err != nil {
			message = err.Error()
		}
	}
	query := url.Values{"saved": {inst}, "applied": {message}}
	if err != nil {
		query.Set("applyError", "1")
	}
	addReply(w, 200, map[string]string{"location": "/setup?" + query.Encode()})
}

// addPlan is a device file prepared under the lock and validated without it.
type addPlan struct {
	token    string
	path     string
	original DeviceSpec
	baseText string // the prototype's text as read, or the template for a new file
	values   map[string]json.RawMessage
	identity map[string]json.RawMessage
	used     map[int]bool
}

// saveAddDevice prepares the file under o.mu, then releases it while the driver validates the
// result and a port is probed, both of which can take seconds, and takes it again to recheck
// and commit.
func (o *orchestrator) saveAddDevice(ctx context.Context, form url.Values) (string, bool, error) {
	fail := func(err error) (string, bool, error) { return "", false, err }
	plan, d, err := o.planAddDevice(form)
	if err != nil {
		return fail(err)
	}

	port, err := availableAddPort(plan.used)
	if err != nil {
		return fail(err)
	}
	values := plan.values
	values["port"] = json.RawMessage(strconv.Itoa(port))
	values["enable"] = json.RawMessage("false")
	var text string
	if d.multiKey != "" {
		text, err = pinBlockPrototype(plan.baseText, d.multiKey, values)
	} else {
		text, err = patchDeviceText(plan.baseText, values)
	}
	if err != nil {
		return fail(err)
	}
	spec, warning, err := validateDeviceEdit(ctx, plan.original, text)
	if err != nil {
		return fail(err)
	}
	if warning != "" {
		return fail(fmt.Errorf("complete the device settings: %s", warning))
	}

	inst, err := o.commitAddDevice(plan, d, spec, text, port)
	if err != nil {
		return fail(err)
	}
	return inst, form.Get("enable") == "true", nil
}

// planAddDevice reads the form against the draft and picks the file to write. Called without
// o.mu held; takes it.
func (o *orchestrator) planAddDevice(form url.Values) (*addPlan, *addDraft, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	d := o.addDrafts[form.Get("token")]
	if d == nil || time.Now().After(d.expires) {
		return nil, nil, fmt.Errorf("device selection expired; find devices again")
	}
	specs, _, err := o.addSpecs()
	if err != nil {
		return nil, nil, err
	}
	values := map[string]json.RawMessage{}
	fields := map[string]addField{}
	for _, f := range d.Fields {
		fields[f.Name] = f
	}
	for key, entries := range form {
		if !strings.HasPrefix(key, "field.") {
			continue
		}
		key = strings.TrimPrefix(key, "field.")
		f, ok := fields[key]
		if !ok || isCommonKey(key) {
			return nil, nil, fmt.Errorf("unknown device field %q", key)
		}
		v := entries[0]
		switch f.Type {
		case "number":
			if v == "" {
				continue
			}
			var n json.Number
			if json.Unmarshal([]byte(v), &n) != nil {
				return nil, nil, fmt.Errorf("%s must be a number", f.Label)
			}
			values[key] = json.RawMessage(v)
		case "checkbox":
			if v != "true" && v != "false" {
				return nil, nil, fmt.Errorf("invalid boolean for %s", key)
			}
			values[key] = json.RawMessage(v)
		default:
			values[key], _ = json.Marshal(v)
		}
	}
	identityValues := values
	if d.Discovery.Supported {
		i, err := strconv.Atoi(form.Get("device"))
		if err != nil || i < 0 || i >= len(d.Discovery.Devices) {
			return nil, nil, fmt.Errorf("select an available device")
		}
		found := d.Discovery.Devices[i]
		identityValues = found.Values
		// Discovery is authoritative; prefer stable identity over an enumeration index.
		key, value := identityValue(found.Values, d.Discovery.Identity)
		if key == "" {
			return nil, nil, fmt.Errorf("the device has no readable identity; release it from other software and scan again")
		}
		for _, k := range d.Discovery.Identity {
			delete(values, k)
		}
		values[key] = value
		for k, v := range found.Values {
			if _, ok := fields[k]; ok && !containsKey(d.Discovery.Identity, k) && !isCommonKey(k) {
				values[k] = v
			}
		}
	}
	if len(d.Discovery.Identity) > 0 {
		if key, _ := identityValue(identityValues, d.Discovery.Identity); key == "" {
			return nil, nil, fmt.Errorf("enter the device connection details")
		}
	}
	if existing := configuredDevice(specs, d.Discovery, identityValues); existing != "" {
		return nil, nil, fmt.Errorf("this device is already configured as %s; edit that entry instead", existing)
	}
	if name := strings.TrimSpace(form.Get("name")); name != "" {
		values["name"], _ = json.Marshal(name)
	}

	plan := &addPlan{token: d.Token, baseText: d.text, values: values, identity: identityValues,
		original: d.original, path: d.original.Source, used: o.usedPorts(specs)}
	if plan.path != "" {
		if err := o.checkPrototype(d, specs); err != nil {
			return nil, nil, err
		}
		return plan, d, nil
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, nil, err
	}
	// Canonical driver names come from a trusted binary, but still must be filename-safe.
	stem := d.Discovery.Driver
	if stem == "" || strings.ContainsAny(stem, `/\`) || strings.HasPrefix(stem, ".") {
		return nil, nil, fmt.Errorf("invalid driver name")
	}
	inst := stem + "-" + hex.EncodeToString(random[:])
	plan.path = filepath.Join(devicesDirFor(o.cfgPath), inst+".json")
	if err := checkNewInstance(plan.path, inst); err != nil {
		return nil, nil, err
	}
	var admin map[string]json.RawMessage
	clean, _ := editorJSON(d.text)
	_ = json.Unmarshal(clean, &admin)
	plan.original, err = deviceSpecFromAdmin(plan.path, "", admin)
	if err != nil {
		return nil, nil, err
	}
	return plan, d, nil
}

// checkPrototype confirms the prototype file is unchanged and still unassigned. Called with
// o.mu held.
func (o *orchestrator) checkPrototype(d *addDraft, specs []DeviceSpec) error {
	path := d.original.Source
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(current) != d.text {
		return fmt.Errorf("the prototype changed; find devices again")
	}
	for _, s := range specs {
		if s.Source != path {
			continue
		}
		assigned := s.enabled()
		for _, block := range deviceIdentityMaps(s.Raw) {
			if key, _ := identityValue(block, d.Discovery.Identity); key != "" {
				assigned = true
			}
		}
		if assigned {
			return fmt.Errorf("the prototype is already in use; find devices again")
		}
	}
	return nil
}

// checkNewInstance confirms neither the device file nor its state file exists yet.
func checkNewInstance(path, inst string) error {
	for _, p := range []string{path, filepath.Join(stateDevicesDir(), inst+".json")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			return fmt.Errorf("instance path already exists or cannot be checked: %s", p)
		}
	}
	return nil
}

// commitAddDevice rechecks what may have changed while the lock was released, then writes the
// file and records the device. Called without o.mu held; takes it.
func (o *orchestrator) commitAddDevice(plan *addPlan, d *addDraft, spec DeviceSpec, text string, port int) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.addDrafts[plan.token] != d {
		return "", fmt.Errorf("device selection expired; find devices again")
	}
	specs, _, err := o.addSpecs()
	if err != nil {
		return "", err
	}
	if o.usedPorts(specs)[port] {
		return "", fmt.Errorf("port %d was taken meanwhile; add the device again", port)
	}
	if existing := configuredDevice(specs, d.Discovery, plan.identity); existing != "" {
		return "", fmt.Errorf("this device is already configured as %s; edit that entry instead", existing)
	}
	if d.original.Source != "" {
		if err := o.checkPrototype(d, specs); err != nil {
			return "", err
		}
	} else if err := checkNewInstance(plan.path, strings.TrimSuffix(filepath.Base(plan.path), ".json")); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(plan.path), 0755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(plan.path, []byte(text)); err != nil {
		return "", err
	}
	replacement := orchRow{spec: spec, res: resolveDriver(spec), port: spec.Port}
	var idxs []int
	for i, row := range o.rows {
		if row.spec.Source == plan.path {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) > 0 {
		o.replaceRows(idxs, []orchRow{replacement})
	} else {
		o.rows = append(o.rows, replacement)
	}
	replaced := false
	for i, s := range o.cfg.Devices {
		if s.Source == plan.path {
			o.cfg.Devices[i] = spec
			replaced = true
		}
	}
	if !replaced {
		o.cfg.Devices = append(o.cfg.Devices, spec)
	}
	delete(o.addDrafts, plan.token)
	return spec.Instance, nil
}

func containsKey(keys []string, key string) bool {
	for _, k := range keys {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// pinBlockPrototype configures the first block of a multi-device template, such as astrocam's
// cameras array, and disables the other unassigned blocks so positional defaults cannot open
// more hardware. It keeps the array and its comments. Keys common to the whole entry, other
// than name, go to the top level.
func pinBlockPrototype(text, multiKey string, values map[string]json.RawMessage) (string, error) {
	clean, err := editorJSON(text)
	if err != nil {
		return "", err
	}
	spans, err := topLevelSpans(clean)
	if err != nil {
		return "", err
	}
	for _, s := range spans {
		if !strings.EqualFold(s.key, multiKey) {
			continue
		}
		array := json.NewDecoder(bytes.NewReader(clean[s.start:s.end]))
		if token, err := array.Token(); err != nil || token != json.Delim('[') {
			return "", fmt.Errorf("%s must be an array", multiKey)
		}
		type span struct{ start, end int }
		var blocks []span
		for array.More() {
			offset := s.start + int(array.InputOffset())
			for offset < len(clean) && strings.ContainsRune(", \t\r\n", rune(clean[offset])) {
				offset++
			}
			var block map[string]json.RawMessage
			if err := array.Decode(&block); err != nil || block == nil {
				return "", fmt.Errorf("invalid %s block", multiKey)
			}
			blocks = append(blocks, span{offset, s.start + int(array.InputOffset())})
		}
		if len(blocks) == 0 {
			return "", fmt.Errorf("%s prototype has no slots", multiKey)
		}
		first := map[string]json.RawMessage{"enable": json.RawMessage("true")}
		root := map[string]json.RawMessage{}
		for key, value := range values {
			if isCommonKey(key) && key != "name" {
				root[key] = value
			} else {
				first[key] = value
			}
		}
		for i := len(blocks) - 1; i >= 0; i-- {
			change := map[string]json.RawMessage{"enable": json.RawMessage("false")}
			if i == 0 {
				change = first
			}
			block, err := patchDeviceText(text[blocks[i].start:blocks[i].end], change)
			if err != nil {
				return "", err
			}
			text = text[:blocks[i].start] + block + text[blocks[i].end:]
		}
		return patchDeviceText(text, root)
	}
	return patchDeviceText(text, values) // an already-flat template
}
