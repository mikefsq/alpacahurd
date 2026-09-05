package hurd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner returns canned output by command line; an ERR prefix returns an error.
type fakeRunner struct {
	calls   []string
	answers map[string]string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if a, ok := f.answers[line]; ok {
		if strings.HasPrefix(a, "ERR ") {
			return "", errors.New(a[4:])
		}
		return a, nil
	}
	return "", nil
}

func (f *fakeRunner) called(t *testing.T, want string) {
	t.Helper()
	for _, c := range f.calls {
		if c == want {
			return
		}
	}
	t.Errorf("command not run: %q\nran:\n  %s", want, strings.Join(f.calls, "\n  "))
}

func TestSystemdSupervisor(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{
		"systemctl cat alpacahurd-device@.service": "[Service]\nExecStart=/usr/local/bin/alpacahurd -launch %i -config /etc/alpacahurd/hurd.json\n",
		"systemctl show alpacahurd-device@roof.service --property=LoadState,ActiveState,SubState,UnitFileState,MainPID,Result": "LoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\nMainPID=4242\nResult=success\n",
		"journalctl -u alpacahurd-device@roof.service -n 3 --no-pager -o short-iso":                                            "line1\nline2\nline3",
	}}
	s := newSystemdSupervisor(f.run, "/usr/local/bin/alpacahurd", "/etc/alpacahurd/hurd.json")
	ctx := context.Background()
	if s.Name() != "systemd" {
		t.Fatal(s.Name())
	}
	if err := s.Install(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "systemctl daemon-reload")
	st, err := s.Status(ctx, "roof")
	if err != nil || !st.Installed || !st.Enabled || !st.Running || st.PID != 4242 || st.State != "active (running)" || st.LastError != "" {
		t.Fatalf("status %+v %v", st, err)
	}
	for action, fn := range map[string]func(context.Context, string) error{
		"start": s.Start, "stop": s.Stop, "restart": s.Restart, "enable": s.Enable, "disable": s.Disable,
	} {
		if err := fn(ctx, "roof"); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		f.called(t, "systemctl "+action+" alpacahurd-device@roof.service")
	}
	if err := s.Uninstall(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "systemctl disable --now alpacahurd-device@roof.service")
	var b strings.Builder
	if err := s.Logs(ctx, "roof", 3, &b); err != nil || !strings.Contains(b.String(), "line3") {
		t.Fatalf("logs %q %v", b.String(), err)
	}

	// A template that does not run -launch is refused at install.
	f.answers["systemctl cat alpacahurd-device@.service"] = "[Service]\nExecStart=/bin/false\n"
	if err := s.Install(ctx, "roof"); err == nil || !strings.Contains(err.Error(), "-launch") {
		t.Fatalf("install against a stale template: %v", err)
	}
	// A failed unit reports its Result.
	f.answers["systemctl show alpacahurd-device@roof.service --property=LoadState,ActiveState,SubState,UnitFileState,MainPID,Result"] = "LoadState=loaded\nActiveState=failed\nSubState=failed\nUnitFileState=disabled\nMainPID=0\nResult=exit-code\n"
	st, _ = s.Status(ctx, "roof")
	if st.Running || st.Enabled || st.LastError != "exit-code" {
		t.Fatalf("failed status %+v", st)
	}
}

func TestLaunchdSupervisor(t *testing.T) {
	t.Setenv("ALPACA_STATE_DIR", "/Library/Application Support/alpacahurd/state")
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	f := &fakeRunner{answers: map[string]string{
		"launchctl print system/com.mikefsq.alpacahurd.roof": "ERR not found",
		"launchctl print-disabled system":                    `	"com.mikefsq.alpacahurd.other" => disabled` + "\n",
	}}
	l := newLaunchdSupervisor(f.run, "/usr/local/bin/alpacahurd", "/Library/Application Support/alpacahurd/hurd.json", dir, logDir)
	ctx := context.Background()

	st, err := l.Status(ctx, "roof")
	if err != nil || st.Installed || st.State != "not installed" {
		t.Fatalf("status before install %+v %v", st, err)
	}
	if err := l.Install(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(filepath.Join(dir, "com.mikefsq.alpacahurd.roof.plist"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>com.mikefsq.alpacahurd.roof</string>",
		"<string>/usr/local/bin/alpacahurd</string>", "<string>-launch</string>", "<string>roof</string>",
		"<string>-config</string>", "<string>/Library/Application Support/alpacahurd/hurd.json</string>",
		"<key>ALPACA_CONFIG_DIR</key>", "<string>/Library/Application Support/alpacahurd</string>",
		"<key>ALPACA_STATE_DIR</key>", "<string>/Library/Application Support/alpacahurd/state</string>",
		"<key>KeepAlive</key>", "<key>StandardOutPath</key>", "<string>" + filepath.Join(logDir, "roof.log") + "</string>",
	} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("plist missing %q", want)
		}
	}
	// Idempotent: a second install rewrites nothing.
	fi1, _ := os.Stat(filepath.Join(dir, "com.mikefsq.alpacahurd.roof.plist"))
	if err := l.Install(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	fi2, _ := os.Stat(filepath.Join(dir, "com.mikefsq.alpacahurd.roof.plist"))
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Error("unchanged plist was rewritten")
	}

	// Not loaded: Start bootstraps.
	if err := l.Start(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "launchctl bootstrap system "+filepath.Join(dir, "com.mikefsq.alpacahurd.roof.plist"))
	// Loaded and running: Status reads it, Start kickstarts, Restart kicks with -k, Stop boots out.
	f.answers["launchctl print system/com.mikefsq.alpacahurd.roof"] = "system/com.mikefsq.alpacahurd.roof = {\n\tactive count = 1\n\tpath = /Library/LaunchDaemons/com.mikefsq.alpacahurd.roof.plist\n\tstate = running\n\tpid = 515\n\tlast exit code = 0\n}\n"
	st, err = l.Status(ctx, "roof")
	if err != nil || !st.Installed || !st.Enabled || !st.Running || st.PID != 515 || st.State != "running" {
		t.Fatalf("status running %+v %v", st, err)
	}
	if err := l.Start(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "launchctl kickstart system/com.mikefsq.alpacahurd.roof")
	if err := l.Restart(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "launchctl kickstart -k system/com.mikefsq.alpacahurd.roof")
	if err := l.Stop(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "launchctl bootout system/com.mikefsq.alpacahurd.roof")
	if err := l.Enable(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "launchctl enable system/com.mikefsq.alpacahurd.roof")
	if err := l.Disable(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "launchctl disable system/com.mikefsq.alpacahurd.roof")
	// A disabled label reads as not enabled; a non-zero last exit is the last error.
	f.answers["launchctl print-disabled system"] = `	"com.mikefsq.alpacahurd.roof" => disabled` + "\n"
	f.answers["launchctl print system/com.mikefsq.alpacahurd.roof"] = "\tstate = waiting\n\tlast exit code = 1\n"
	st, _ = l.Status(ctx, "roof")
	if st.Enabled || st.Running || st.State != "waiting" || st.LastError != "last exit 1" {
		t.Fatalf("disabled status %+v", st)
	}
	// Logs tail the job's file.
	os.WriteFile(filepath.Join(logDir, "roof.log"), []byte("a\nb\nc\nd\n"), 0o644)
	var b strings.Builder
	if err := l.Logs(ctx, "roof", 2, &b); err != nil || b.String() != "c\nd\n" {
		t.Fatalf("logs %q %v", b.String(), err)
	}
	if err := l.Uninstall(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "com.mikefsq.alpacahurd.roof.plist")); !errors.Is(err, os.ErrNotExist) {
		t.Error("plist not removed")
	}
}

func TestSCMSupervisor(t *testing.T) {
	logDir := t.TempDir()
	f := &fakeRunner{answers: map[string]string{
		"sc.exe qc alpacahurd-roof": "ERR [SC] OpenService FAILED 1060: The specified service does not exist",
	}}
	s := newSCMSupervisor(f.run, `C:\Program Files\alpacahurd\alpacahurd.exe`, `C:\ProgramData\alpacahurd\hurd.json`, logDir)
	ctx := context.Background()

	st, err := s.Status(ctx, "roof")
	if err != nil || st.Installed {
		t.Fatalf("status before install %+v %v", st, err)
	}
	if err := s.Install(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, `sc.exe create alpacahurd-roof binPath= "C:\Program Files\alpacahurd\alpacahurd.exe" "-launch" "roof" "-config" "C:\ProgramData\alpacahurd\hurd.json" start= auto DisplayName= alpacahurd device roof`)
	f.called(t, "sc.exe failure alpacahurd-roof reset= 60 actions= restart/5000/restart/5000/restart/5000")
	// Installed and running.
	f.answers["sc.exe qc alpacahurd-roof"] = "[SC] QueryServiceConfig SUCCESS\n\nSERVICE_NAME: alpacahurd-roof\n        TYPE               : 10  WIN32_OWN_PROCESS\n        START_TYPE         : 2   AUTO_START\n"
	f.answers["sc.exe query alpacahurd-roof"] = "SERVICE_NAME: alpacahurd-roof\n        TYPE               : 10  WIN32_OWN_PROCESS\n        STATE              : 4  RUNNING\n                                (STOPPABLE, NOT_PAUSABLE, ACCEPTS_SHUTDOWN)\n        WIN32_EXIT_CODE    : 0  (0x0)\n        PID                : 3120\n"
	st, err = s.Status(ctx, "roof")
	if err != nil || !st.Installed || !st.Enabled || !st.Running || st.State != "RUNNING" || st.PID != 3120 {
		t.Fatalf("status running %+v %v", st, err)
	}
	// A second install reconfigures rather than recreates.
	if err := s.Install(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, `sc.exe config alpacahurd-roof binPath= "C:\Program Files\alpacahurd\alpacahurd.exe" "-launch" "roof" "-config" "C:\ProgramData\alpacahurd\hurd.json"`)
	if err := s.Start(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "sc.exe start alpacahurd-roof")
	if err := s.Enable(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "sc.exe config alpacahurd-roof start= auto")
	if err := s.Disable(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "sc.exe config alpacahurd-roof start= demand")
	// Stop waits for the state to leave RUNNING.
	f.answers["sc.exe query alpacahurd-roof"] = "        STATE              : 1  STOPPED\n        WIN32_EXIT_CODE    : 0  (0x0)\n"
	if err := s.Stop(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "sc.exe stop alpacahurd-roof")
	os.WriteFile(filepath.Join(logDir, "roof.log"), []byte("x\ny\n"), 0o644)
	var b strings.Builder
	if err := s.Logs(ctx, "roof", 5, &b); err != nil || b.String() != "x\ny\n" {
		t.Fatalf("logs %q %v", b.String(), err)
	}
	if err := s.Uninstall(ctx, "roof"); err != nil {
		t.Fatal(err)
	}
	f.called(t, "sc.exe delete alpacahurd-roof")
}

func TestLaunchArgv(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "hurd.json")
	writeFile(t, cfgPath, `{"discovery":"off"}`)
	bin := filepath.Join(t.TempDir(), "widget")
	os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	writeFile(t, filepath.Join(root, "devices.d", "wid.json"), `{"driver":"widget","exec":"`+bin+`","port":11601}`)
	writeFile(t, filepath.Join(root, "devices.d", "foc.json"), `{"driver":"sim-focuser","port":11600}`)
	writeFile(t, filepath.Join(root, "devices.d", "gone.json"), `{"driver":"gone-driver","port":11602}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	exe, args, err := launchArgv(cfg, cfgPath, "wid")
	if err != nil || exe != bin {
		t.Fatalf("wid: %q %v %v", exe, args, err)
	}
	want := []string{"-discovery", "register", "-config", filepath.Join(root, "devices.d", "wid.json")}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("wid args %v, want %v", args, want)
	}
	if _, _, err := launchArgv(cfg, cfgPath, "foc"); err == nil || !strings.Contains(err.Error(), "compiled into") {
		t.Fatalf("compiled-in: %v", err)
	}
	if _, _, err := launchArgv(cfg, cfgPath, "gone"); err == nil || !strings.Contains(err.Error(), "no binary") {
		t.Fatalf("unresolved: %v", err)
	}
	if _, _, err := launchArgv(cfg, cfgPath, "nope"); err == nil || !strings.Contains(err.Error(), "no device") {
		t.Fatalf("unknown: %v", err)
	}
	if got := launchCommand("/usr/local/bin/alpacahurd", cfgPath, "wid"); strings.Join(got, " ") != "/usr/local/bin/alpacahurd -launch wid -config "+cfgPath {
		t.Fatalf("launchCommand %v", got)
	}
}
