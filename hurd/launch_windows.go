//go:build windows

package hurd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"golang.org/x/sys/windows/svc"
)

// execDriver runs a child process, forwarding console I/O or serving SCM requests.
// Under SCM, output goes to the instance log and Stop terminates the child.
func execDriver(exe string, args []string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		cmd := exec.Command(exe, args...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		return cmd.Run()
	}
	name := scmServicePrefix + instanceFromArgs(args)
	return svc.Run(name, &driverHost{exe: exe, args: args, name: name})
}

// instanceFromArgs returns the device filename stem from -config.
func instanceFromArgs(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-config" {
			base := filepath.Base(args[i+1])
			return base[:len(base)-len(filepath.Ext(base))]
		}
	}
	return "device"
}

// driverHost runs a driver as an SCM service child.
type driverHost struct {
	exe  string
	args []string
	name string
}

func (h *driverHost) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	logDir := alpacadev.LogDir("alpacahurd")
	_ = os.MkdirAll(logDir, 0o755)
	logFile, err := os.OpenFile(filepath.Join(logDir, h.name[len(scmServicePrefix):]+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		status <- svc.Status{State: svc.Stopped}
		return true, 1
	}
	defer logFile.Close()
	cmd := exec.Command(h.exe, h.args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(logFile, "%s alpacahurd: start %s: %v\n", time.Now().Format(time.RFC3339), h.exe, err)
		status <- svc.Status{State: svc.Stopped}
		return true, 1
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-exited:
			// Report failure so SCM recovery restarts the service.
			fmt.Fprintf(logFile, "%s alpacahurd: %s exited: %v\n", time.Now().Format(time.RFC3339), h.exe, err)
			status <- svc.Status{State: svc.Stopped}
			return true, 1
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				_ = cmd.Process.Kill()
				<-exited
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		}
	}
}
