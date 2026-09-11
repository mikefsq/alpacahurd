package hurd

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func editorFixture(t *testing.T) (*orchestrator, string, string) {
	t.Helper()
	t.Setenv("ALPACA_STATE_DIR", t.TempDir())
	root := t.TempDir()
	path := filepath.Join(root, "devices.d", "cam.json")
	original := "{\n  \"driver\": \"sim-camera\",\n  \"enable\": false\n}\n"
	writeFile(t, path, original)
	spec, err := loadDeviceFile(path, stateDevicesDir())
	if err != nil {
		t.Fatal(err)
	}
	o := &orchestrator{cfgPath: filepath.Join(root, "hurd.json"), cfg: &Config{Devices: []DeviceSpec{spec}}, sup: noSupervisor{}, startedAt: time.Now(), rows: []orchRow{{spec: spec, res: resolveDriver(spec)}}}
	return o, path, original
}

func editorPost(o *orchestrator, path, draft string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"instance": {"cam"}, "text": {draft}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	o.ServeHTTP(w, r)
	return w
}

func TestEditorRejectsInvalidDraftBeforeWriting(t *testing.T) {
	for _, draft := range []string{
		`{"driver":"sim-camera",`,
		`null`, `[]`, `{}`, "{\"driver\":\"sim-camera\"} /*\n", `{"driver":true}`, `{"driver":"sim-camera","port":"bad"}`,
		`{"driver":"sim-camera","port":70000}`, `{"driver":"sim-camera","device":-1}`,
		`{"driver":"sim-camera","enable":true,"pixelCountX":"bad"}`,
		`{"driver":"sim-camera","enable":true,"typo":123}`,
		`{"driver":"not-an-installed-editor-test-driver"}`,
	} {
		t.Run(draft, func(t *testing.T) {
			o, path, original := editorFixture(t)
			check := editorPost(o, "/setup/edit/check", draft)
			if check.Code != http.StatusUnprocessableEntity {
				t.Fatalf("check: %d %s", check.Code, check.Body.String())
			}
			w := editorPost(o, "/setup/edit", draft)
			if !strings.Contains(w.Body.String(), "not saved") || !strings.Contains(w.Body.String(), `id="config-editor"`) || !strings.Contains(w.Body.String(), html.EscapeString(draft)) {
				t.Fatalf("draft/error missing: %s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "<table>") || w.Header().Get("Location") != "" {
				t.Fatal("failed save left the dedicated editor")
			}
			got, _ := os.ReadFile(path)
			if string(got) != original {
				t.Fatal("invalid draft replaced original")
			}
			if string(o.cfg.Devices[0].Raw) != string(o.rows[0].spec.Raw) {
				t.Fatal("in-memory configuration changed")
			}
		})
	}
}

func TestEditorCheckDoesNotSaveAndSaveRevalidates(t *testing.T) {
	o, path, original := editorFixture(t)
	draft := "{\n// keep this comment\n\"driver\":\"sim-camera\",\"pixelCountX\":640,\"enable\":false\n}"
	w := editorPost(o, "/setup/edit/check", draft)
	var result struct {
		Valid   bool
		Message string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Valid {
		t.Fatalf("check: %s (%v)", w.Body.String(), err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatal("check wrote the draft")
	}
	// A successful check cannot authorize a different, invalid submission.
	editorPost(o, "/setup/edit", `{"driver":"sim-camera","pixelCountX":"bad"}`)
	got, _ = os.ReadFile(path)
	if string(got) != original {
		t.Fatal("save trusted prior validation")
	}
	w = editorPost(o, "/setup/edit", draft)
	got, _ = os.ReadFile(path)
	if string(got) != draft+"\n" {
		t.Fatalf("valid draft not saved: %s", w.Body.String())
	}
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/setup?saved=cam" {
		t.Fatalf("save did not redirect to the list: %d %s", w.Code, w.Header().Get("Location"))
	}
	list := httptest.NewRecorder()
	o.ServeHTTP(list, httptest.NewRequest(http.MethodGet, w.Header().Get("Location"), nil))
	if !strings.Contains(list.Body.String(), "saved "+path) || strings.Contains(list.Body.String(), `id="config-editor"`) {
		t.Fatal("redirect destination must show the list and save confirmation")
	}
	// Refreshing the destination is a GET, so it cannot write configuration again.
	writeFile(t, path, original)
	o.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, w.Header().Get("Location"), nil))
	got, _ = os.ReadFile(path)
	if string(got) != original {
		t.Fatal("refresh repeated the save")
	}
}

func TestEditorDedicatedPage(t *testing.T) {
	o, _, original := editorFixture(t)
	w := httptest.NewRecorder()
	o.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/setup/edit?instance=cam", nil))
	for _, want := range []string{`<title>Edit cam`, `<h1>Edit cam</h1>`, `href="/setup">← Back to devices`, `href="/setup">Cancel`, html.EscapeString(original), `id="check-config"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("editor missing %q", want)
		}
	}
	for _, unwanted := range []string{"<table>", "<h2>Add a device</h2>", "/setup/check", "supervisor "} {
		if strings.Contains(w.Body.String(), unwanted) {
			t.Fatalf("editor contains inventory content %q", unwanted)
		}
	}
	w = httptest.NewRecorder()
	o.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/setup/edit?instance=missing", nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "Back to devices") || strings.Contains(w.Body.String(), "<table>") {
		t.Fatal("unknown editor should show an error with navigation back")
	}
}

func TestEditorCheckValidatesStateOverlay(t *testing.T) {
	o, path, original := editorFixture(t)
	writeFile(t, filepath.Join(stateDevicesDir(), "cam.json"), `{"pixelCountX":"broken"}`)
	w := editorPost(o, "/setup/edit/check", `{"driver":"sim-camera"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad overlay accepted: %s", w.Body.String())
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatal("check wrote file")
	}
	// An explicit valid replacement for the bad overlay value is repairable.
	w = editorPost(o, "/setup/edit/check", `{"driver":"sim-camera","pixelCountX":640}`)
	if w.Code != http.StatusOK {
		t.Fatalf("overlay repair refused: %s", w.Body.String())
	}
}

func TestEditorCheckUnknownInstanceAndMethod(t *testing.T) {
	o, _, _ := editorFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/setup/edit/check", nil)
	w := httptest.NewRecorder()
	o.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/setup/edit/check", strings.NewReader("instance=missing&text=%7B%7D"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	o.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "no device file") {
		t.Fatal(w.Body.String())
	}
}

func TestEditorStandaloneChecker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	o, path, original := editorFixture(t)
	exe := filepath.Join(t.TempDir(), "checker")
	script := "#!/bin/sh\n[ \"$1\" = -config ] && [ \"$3\" = -check ] || exit 9\ncat \"$2\" > \"" + exe + ".input\"\nexit 0\n"
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	spec := o.rows[0].spec
	spec.Driver = "editor-external-test"
	spec.Exec = exe
	raw, _ := json.Marshal(map[string]any{"driver": spec.Driver, "exec": exe, "name": "Proposed"})
	spec.Raw = raw
	o.rows[0].spec = spec
	w := editorPost(o, "/setup/edit/check", string(raw))
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	input, _ := os.ReadFile(exe + ".input")
	if !strings.Contains(string(input), "Proposed") {
		t.Fatal("checker did not receive draft")
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatal("checker replaced original")
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho invalid selector >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	w = editorPost(o, "/setup/edit/check", string(raw))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "invalid selector") {
		t.Fatal(w.Body.String())
	}
	editorPost(o, "/setup/edit", string(raw))
	got, _ = os.ReadFile(path)
	if string(got) != original {
		t.Fatal("failed checker allowed save")
	}
	// A draft must not cause a newly supplied executable to run.
	other := filepath.Join(t.TempDir(), "new-checker")
	os.WriteFile(other, []byte("#!/bin/sh\ntouch "+other+".ran\n"), 0700)
	proposed, _ := json.Marshal(map[string]any{"driver": spec.Driver, "exec": other})
	w = editorPost(o, "/setup/edit/check", string(proposed))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(other + ".ran"); !os.IsNotExist(err) {
		t.Fatal("executed unchecked draft command")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkStandalone(ctx, exe, raw); err == nil {
		t.Fatal("cancelled check succeeded")
	}
}

func TestEditorDisabledDraftAndEnableOverride(t *testing.T) {
	o, path, _ := editorFixture(t)
	draft := `{"driver":"sim-camera","enable":false,"pixelCountX":"unfinished"}`
	w := editorPost(o, "/setup/edit/check", draft)
	var result struct {
		Valid   bool
		Ready   bool
		Message string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || !result.Valid || result.Ready || !strings.Contains(result.Message, "not ready to run") {
		t.Fatalf("draft check: %d %s", w.Code, w.Body.String())
	}
	w = editorPost(o, "/setup/edit", draft)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "draft=1") {
		t.Fatalf("draft save: %d %s", w.Code, w.Body.String())
	}
	saved, _ := os.ReadFile(path)
	if string(saved) != draft+"\n" {
		t.Fatal("disabled draft not preserved")
	}
	list := httptest.NewRecorder()
	o.ServeHTTP(list, httptest.NewRequest(http.MethodGet, w.Header().Get("Location"), nil))
	if !strings.Contains(list.Body.String(), "Draft saved") || !strings.Contains(list.Body.String(), "banner warn") {
		t.Fatal("draft reported as ready")
	}
	// Legacy state must not override the disabled configuration.
	writeFile(t, filepath.Join(stateDevicesDir(), "cam.json"), `{"enable":true}`)
	w = editorPost(o, "/setup/edit/check", draft)
	if w.Code != http.StatusOK {
		t.Fatal("legacy state overrode disabled draft")
	}
	enabled := strings.Replace(draft, `"enable":false`, `"enable":true`, 1)
	w = editorPost(o, "/setup/edit", enabled)
	if w.Code == http.StatusSeeOther {
		t.Fatal("invalid enabled draft saved")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(saved) {
		t.Fatal("invalid enable replaced file")
	}

}

func TestEditorStandaloneIncompleteDisabledDraft(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	o, path, _ := editorFixture(t)
	exe := filepath.Join(t.TempDir(), "checker")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho 'driver requires serial or addr' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	spec := o.rows[0].spec
	spec.Driver, spec.Exec = "incomplete-external-test", exe
	raw, _ := json.Marshal(map[string]any{"driver": spec.Driver, "exec": exe, "enable": false})
	spec.Raw = raw
	o.rows[0].spec = spec
	w := editorPost(o, "/setup/edit/check", string(raw))
	var result struct {
		Valid, Ready bool
		Message      string
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if !result.Valid || result.Ready || !strings.Contains(result.Message, "requires serial or addr") {
		t.Fatal(w.Body.String())
	}
	w = editorPost(o, "/setup/edit", string(raw))
	if w.Code != http.StatusSeeOther {
		t.Fatal(w.Body.String())
	}
	saved, _ := os.ReadFile(path)
	enabled := strings.Replace(string(raw), `"enable":false`, `"enable":true`, 1)
	w = editorPost(o, "/setup/edit", enabled)
	if w.Code == http.StatusSeeOther {
		t.Fatal("incomplete enabled draft saved")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(saved) {
		t.Fatal("enabled draft replaced file")
	}
}

func TestManagementNavigationAndAddPage(t *testing.T) {
	o, _, _ := editorFixture(t)
	page := logGet(o, "/setup").Body.String()
	for _, link := range []string{`href="/setup/logs"`, `href="/setup/check"`, `href="/setup/add"`} {
		if pos := strings.Index(page, link); pos < 0 || pos > strings.Index(page, "<table>") {
			t.Fatalf("navigation link missing above list: %s", link)
		}
	}
	if strings.Contains(page, `action="/setup/add"`) || strings.Contains(page, "/setup/logs?instance=") || strings.Count(page, `href="/setup/logs"`) != 1 {
		t.Fatal("main page still has embedded add form or per-row logs")
	}
	page = logGet(o, "/setup/add").Body.String()
	for _, want := range []string{"<h1>Add a device</h1>", "Back to devices", `action="/setup/add"`, "sim-camera", "Create and configure"} {
		if !strings.Contains(page, want) {
			t.Fatalf("add page missing %s", want)
		}
	}
	if strings.Contains(page, "<table>") {
		t.Fatal("add page contains inventory")
	}
	r := httptest.NewRequest(http.MethodPost, "/setup/add", strings.NewReader(url.Values{"driver": {"sim-camera"}, "instance": {"cam"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	o.ServeHTTP(w, r)
	for _, want := range []string{"exists already", `value="cam"`, `value="sim-camera" selected`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("failed add lost error or draft: %s", want)
		}
	}
}
