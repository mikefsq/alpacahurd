package hurd

import (
	"context"
	"encoding/json"
	"errors"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type logTestSupervisor struct {
	noSupervisor
	instance string
	lines    int
	calls    int
	fail     bool
}

func (s *logTestSupervisor) Name() string { return "test supervisor" }
func (s *logTestSupervisor) Logs(_ context.Context, instance string, n int, w io.Writer) error {
	s.instance, s.lines = instance, n
	s.calls++
	if s.fail {
		return errors.New("journal unavailable")
	}
	_, err := io.WriteString(w, "camera message <script>not HTML</script>\n")
	return err
}
func logFixture(t *testing.T) (*orchestrator, *logTestSupervisor) {
	o, _, _ := editorFixture(t)
	sup := &logTestSupervisor{}
	o.sup = sup
	o.rows[0].res = resolution{kind: installedBinary}
	return o, sup
}
func logGet(o *orchestrator, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	o.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}
func TestLogPageAndInstanceTail(t *testing.T) {
	o, sup := logFixture(t)
	// Exercise goalpaca's host-page routing as well as the dedicated template.
	srv := alpacadev.New(alpacadev.Config{SetupHome: o, SetupPages: o.setupPages()})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/setup/logs?instance=cam", nil))
	for _, want := range []string{"Logs: cam", "This instance only", "log-pause", "log-follow"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("page missing %q", want)
		}
	}
	if strings.Contains(w.Body.String(), "<table>") || sup.calls != 0 {
		t.Fatal("log page should load independently of inventory and journal")
	}
	w = logGet(o, "/setup/logs/tail?instance=cam")
	var result struct{ Text, Updated string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || sup.instance != "cam" || sup.lines != 200 || result.Updated == "" || !strings.Contains(result.Text, "<script>") {
		t.Fatalf("tail: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("log response must not be cached")
	}
	w = logGet(o, "/setup")
	if !strings.Contains(w.Body.String(), `href="/setup/logs" target="_blank" rel="noopener"`) {
		t.Fatal("logs must open in a new tab")
	}
	sup.fail = true
	w = logGet(o, "/setup/logs/tail?instance=cam")
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "journal unavailable") {
		t.Fatal(w.Body.String())
	}
}
func TestLogsRejectUnknownInProcessAndRemote(t *testing.T) {
	o, sup := logFixture(t)
	for _, instance := range []string{"missing", "..%2Fother"} {
		w := logGet(o, "/setup/logs/tail?instance="+instance)
		if w.Code != http.StatusNotFound {
			t.Fatal(w.Code)
		}
	}
	o.rows[0].inProcess = true
	w := logGet(o, "/setup/logs?instance=cam")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "shared log") {
		t.Fatal(w.Body.String())
	}
	o.rows[0].inProcess = false
	o.rows[0].reg = &alpacadev.Registration{Local: false}
	w = logGet(o, "/setup/logs/tail?instance=cam")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "another host") {
		t.Fatal(w.Body.String())
	}
	if sup.calls != 0 {
		t.Fatal("unavailable instance queried the local journal")
	}
}

func TestLogSelectorAndAllInstances(t *testing.T) {
	o, sup := logFixture(t)
	second := o.rows[0]
	second.spec.Instance = "mount"
	remote := second
	remote.spec.Instance = "remote"
	remote.reg = &alpacadev.Registration{Local: false}
	inside := second
	inside.spec.Instance = "inside"
	inside.inProcess = true
	o.rows = append(o.rows, second, second, remote, inside)
	w := logGet(o, "/setup/logs?instance=cam")
	for _, want := range []string{`method="get" action="/setup/logs"`, `>All devices</option>`, `value="cam" selected`, `value="mount"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("selector missing %q", want)
		}
	}
	for _, unwanted := range []string{`value="remote"`, `value="inside"`} {
		if strings.Contains(w.Body.String(), unwanted) {
			t.Fatal("selector offers unavailable log")
		}
	}
	w = logGet(o, "/setup/logs/tail")
	var result struct{ Text string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || sup.calls != 2 || !strings.Contains(result.Text, "--- cam ---") || !strings.Contains(result.Text, "--- mount ---") {
		t.Fatalf("all instance tails: %d %s", w.Code, w.Body.String())
	}
	// An unavailable selection still offers the selector, allowing recovery.
	w = logGet(o, "/setup/logs?instance=missing")
	if !strings.Contains(w.Body.String(), `id="log-instance"`) {
		t.Fatal("error page lost selector")
	}
}

func TestSystemdAllLogsFilter(t *testing.T) {
	o, _ := logFixture(t)
	cmd := "journalctl -u alpacahurd.service -u alpacahurd-device@*.service -n 200 --no-pager -o short-iso"
	f := &fakeRunner{answers: map[string]string{cmd: "combined device logs"}}
	o.sup = newSystemdSupervisor(f.run, "alpacahurd", "hurd.json")
	w := logGet(o, "/setup/logs/tail")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "combined device logs") {
		t.Fatal(w.Body.String())
	}
	f.called(t, cmd)
	w = logGet(o, "/setup/logs")
	if !strings.Contains(w.Body.String(), "All devices") || !strings.Contains(w.Body.String(), "time order") {
		t.Fatal("all log scope missing")
	}
}
func TestSystemdLogViewerFiltersUnit(t *testing.T) {
	o, _ := logFixture(t)
	cmd := "journalctl -u alpacahurd-device@cam.service -n 200 --no-pager -o short-iso"
	f := &fakeRunner{answers: map[string]string{cmd: "selected instance only"}}
	o.sup = newSystemdSupervisor(f.run, "alpacahurd", "hurd.json")
	w := logGet(o, "/setup/logs/tail?instance=cam")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "selected instance only") {
		t.Fatal(w.Body.String())
	}
	f.called(t, cmd)
}
