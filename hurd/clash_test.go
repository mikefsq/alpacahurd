package hurd

import (
	"strings"
	"testing"
)

func TestPortClashes(t *testing.T) {
	on, off := true, false
	row := func(inst string, port int, kind resolutionKind, inProcess bool, enabled *bool) orchRow {
		r := orchRow{port: port, inProcess: inProcess, res: resolution{kind: kind}}
		r.spec.Instance, r.spec.Enable = inst, enabled
		return r
	}
	rows := []orchRow{
		row("a174", 11320, installedBinary, false, &on),
		row("b6200", 11320, installedBinary, false, &on),
		row("c290", 11321, installedBinary, false, &on),
		row("d6200mc", 11321, installedBinary, false, &off), // disabled: no clash
		row("e1", 11400, compiledIn, true, &on),
		row("e2", 11400, compiledIn, true, &on), // one server hosts both
		row("f", 11401, compiledIn, true, &on),
		row("g", 11401, installedBinary, false, &on),
	}
	got := portClashes(rows)
	if len(got) != 2 || !strings.Contains(got[0], "11320: a174 and b6200") || !strings.Contains(got[1], "11401: f and g") {
		t.Fatalf("%q", got)
	}
}

func TestRestartHurdAction(t *testing.T) {
	o, _, _ := editorFixture(t)
	called := make(chan struct{}, 1)
	o.restartSelf = func() { called <- struct{}{} }
	w := addRequest(o, "/setup", map[string][]string{"action": {"restart-hurd"}})
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	<-called
	o.restartSelf = nil
	w = addRequest(o, "/setup", map[string][]string{"action": {"restart-hurd"}})
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
}
