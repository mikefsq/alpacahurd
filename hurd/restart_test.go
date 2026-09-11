package hurd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type restartSupervisor struct {
	noSupervisor
	calls []string
}

func (s *restartSupervisor) Restart(_ context.Context, inst string) error {
	s.calls = append(s.calls, inst)
	return nil
}
func TestRestartStandaloneAction(t *testing.T) {
	o, _, _ := editorFixture(t)
	sup := &restartSupervisor{}
	o.sup = sup
	o.rows[0].res = resolution{kind: installedBinary}
	on := true
	o.rows[0].spec.Enable = &on
	for _, action := range []string{"restart", "reload"} {
		r := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(url.Values{"instance": {"cam"}, "action": {action}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		o.ServeHTTP(w, r)
		if !strings.Contains(w.Body.String(), "restart cam: done") {
			t.Fatal(w.Body.String())
		}
	}
	if len(sup.calls) != 2 {
		t.Fatal(sup.calls)
	}
	on = false
	if err := o.restart(context.Background(), "cam"); err == nil {
		t.Fatal("disabled restart accepted")
	}
	if err := o.restart(context.Background(), "missing"); err == nil {
		t.Fatal("unknown restart accepted")
	}
	if len(sup.calls) != 2 {
		t.Fatal("unexpected service action")
	}
}
