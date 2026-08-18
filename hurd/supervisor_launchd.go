package hurd

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// launchdLabelPrefix names a device's launchd job: com.mikefsq.alpacahurd.<instance>.
const launchdLabelPrefix = "com.mikefsq.alpacahurd."

// launchdSupervisor drives launchd over launchctl. launchd has no template
// jobs, so Install writes one plist per instance under dir (the system
// LaunchDaemons directory), running `alpacahurd -launch <instance>` with the
// same environment the orchestrator's own plist sets. KeepAlive on failure
// supplies the restart; the job's stdout and stderr go to logDir/<instance>.log,
// which Logs reads.
type launchdSupervisor struct {
	run     runner
	self    string
	cfgPath string
	dir     string // where the plists go: /Library/LaunchDaemons
	logDir  string // where the jobs log: /Library/Logs/alpacahurd
}

func newLaunchdSupervisor(run runner, self, cfgPath, dir, logDir string) *launchdSupervisor {
	return &launchdSupervisor{run: run, self: self, cfgPath: cfgPath, dir: dir, logDir: logDir}
}

func (l *launchdSupervisor) Name() string { return "launchd" }

func (l *launchdSupervisor) label(instance string) string { return launchdLabelPrefix + instance }
func (l *launchdSupervisor) plistPath(instance string) string {
	return filepath.Join(l.dir, l.label(instance)+".plist")
}
func (l *launchdSupervisor) logPath(instance string) string {
	return filepath.Join(l.logDir, instance+".log")
}

// plist renders the job. The environment names the config directory the
// orchestrator's file lives in and the state directory the orchestrator itself
// resolved, so the device finds the same files as the orchestrator does.
func (l *launchdSupervisor) plist(instance string) string {
	var args strings.Builder
	for _, a := range launchCommand(l.self, l.cfgPath, instance) {
		fmt.Fprintf(&args, "\t\t<string>%s</string>\n", html.EscapeString(a))
	}
	cfgDir := filepath.Dir(l.cfgPath)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by alpacahurd for the device file %s. Edit the device file, not this;
     alpacahurd rewrites it on install. -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>ALPACA_SYSTEM_SERVICE</key>
		<string>true</string>
		<key>ALPACA_CONFIG_DIR</key>
		<string>%s</string>
		<key>ALPACA_STATE_DIR</key>
		<string>%s</string>
		<key>ALPACA_LOG_DIR</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>5</integer>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, html.EscapeString(instance), html.EscapeString(l.label(instance)), args.String(),
		html.EscapeString(cfgDir), html.EscapeString(stateDirRoot()),
		html.EscapeString(l.logDir), html.EscapeString(l.logPath(instance)), html.EscapeString(l.logPath(instance)))
}

// Install writes the plist and loads it, without starting the job (bootstrap
// with RunAtLoad starts it; a job already loaded is left alone). Idempotent:
// an unchanged plist is not rewritten.
func (l *launchdSupervisor) Install(ctx context.Context, instance string) error {
	want := l.plist(instance)
	path := l.plistPath(instance)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		return nil
	}
	if err := os.MkdirAll(l.logDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		return err
	}
	return nil
}

// Uninstall unloads the job and removes its plist.
func (l *launchdSupervisor) Uninstall(ctx context.Context, instance string) error {
	_, _ = l.run(ctx, "launchctl", "bootout", "system/"+l.label(instance))
	err := os.Remove(l.plistPath(instance))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Start loads the job (which starts it, RunAtLoad) or kicks it if loaded.
func (l *launchdSupervisor) Start(ctx context.Context, instance string) error {
	if _, err := os.Stat(l.plistPath(instance)); err != nil {
		return fmt.Errorf("%s is not installed with launchd", instance)
	}
	if !l.loaded(ctx, instance) {
		_, err := l.run(ctx, "launchctl", "bootstrap", "system", l.plistPath(instance))
		return err
	}
	_, err := l.run(ctx, "launchctl", "kickstart", "system/"+l.label(instance))
	return err
}

// Stop unloads the job. KeepAlive would restart a killed process, so stopping
// means removing the job from launchd until Start loads it again; the plist
// stays, and the job comes back at boot if it is enabled.
func (l *launchdSupervisor) Stop(ctx context.Context, instance string) error {
	if !l.loaded(ctx, instance) {
		return nil
	}
	_, err := l.run(ctx, "launchctl", "bootout", "system/"+l.label(instance))
	return err
}

func (l *launchdSupervisor) Restart(ctx context.Context, instance string) error {
	if !l.loaded(ctx, instance) {
		return l.Start(ctx, instance)
	}
	_, err := l.run(ctx, "launchctl", "kickstart", "-k", "system/"+l.label(instance))
	return err
}

// Enable and Disable use launchd's persistent per-label override, which
// survives reboots and gates bootstrap at boot.
func (l *launchdSupervisor) Enable(ctx context.Context, instance string) error {
	_, err := l.run(ctx, "launchctl", "enable", "system/"+l.label(instance))
	return err
}

func (l *launchdSupervisor) Disable(ctx context.Context, instance string) error {
	_, err := l.run(ctx, "launchctl", "disable", "system/"+l.label(instance))
	return err
}

// Status combines the plist's presence, launchctl print for the running
// state, and print-disabled for the boot gate.
func (l *launchdSupervisor) Status(ctx context.Context, instance string) (InstanceStatus, error) {
	var st InstanceStatus
	if _, err := os.Stat(l.plistPath(instance)); err != nil {
		st.State = "not installed"
		return st, nil
	}
	st.Installed = true
	st.Enabled = true
	if out, err := l.run(ctx, "launchctl", "print-disabled", "system"); err == nil {
		if launchdDisabled(out, l.label(instance)) {
			st.Enabled = false
		}
	}
	out, err := l.run(ctx, "launchctl", "print", "system/"+l.label(instance))
	if err != nil {
		st.State = "not loaded"
		return st, nil
	}
	state, pid, lastExit := parseLaunchctlPrint(out)
	st.State = state
	st.PID = pid
	st.Running = state == "running"
	if lastExit != "" && lastExit != "0" {
		st.LastError = "last exit " + lastExit
	}
	return st, nil
}

// Logs tails the job's log file.
func (l *launchdSupervisor) Logs(_ context.Context, instance string, n int, w io.Writer) error {
	return tailFile(l.logPath(instance), n, w)
}

func (l *launchdSupervisor) loaded(ctx context.Context, instance string) bool {
	_, err := l.run(ctx, "launchctl", "print", "system/"+l.label(instance))
	return err == nil
}

var (
	launchdStateRe = regexp.MustCompile(`(?m)^\s*state = (\S+)`)
	launchdPIDRe   = regexp.MustCompile(`(?m)^\s*pid = (\d+)`)
	launchdExitRe  = regexp.MustCompile(`(?m)^\s*last exit code = (\S+)`)
)

// parseLaunchctlPrint reads the fields the page shows out of launchctl print.
func parseLaunchctlPrint(out string) (state string, pid int, lastExit string) {
	if m := launchdStateRe.FindStringSubmatch(out); m != nil {
		state = m[1]
	}
	if m := launchdPIDRe.FindStringSubmatch(out); m != nil {
		pid, _ = strconv.Atoi(m[1])
	}
	if m := launchdExitRe.FindStringSubmatch(out); m != nil {
		lastExit = m[1]
	}
	if state == "" {
		state = "loaded"
	}
	return
}

// launchdDisabled reports whether print-disabled lists label as disabled.
// The output is lines of the form `"label" => disabled`.
func launchdDisabled(out, label string) bool {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"`+label+`"`) && strings.HasSuffix(line, "disabled") {
			return true
		}
	}
	return false
}

// tailFile writes the last n lines of path to w; a missing file writes a note.
func tailFile(path string, n int, w io.Writer) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, err = fmt.Fprintf(w, "no log yet at %s\n", path)
		}
		return err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	_, err = io.WriteString(w, strings.Join(lines, "\n")+"\n")
	return err
}
