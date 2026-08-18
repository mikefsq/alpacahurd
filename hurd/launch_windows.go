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

// execDriver runs the driver binary as a child. Under the SCM this process is
// the service, so it answers the SCM's control requests and stops the child on
// Stop and Shutdown; from a console it waits for the child and returns its
// exit. The child's output goes to <LogDir>/<service>.log under the SCM and to
// this process's stdout and stderr from a console.
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

// instanceFromArgs recovers the instance from the -config file the driver is
// launched with, for the log file name.
func instanceFromArgs(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-config" {
			base := filepath.Base(args[i+1])
			return base[:len(base)-len(filepath.Ext(base))]
		}
	}
	return "device"
}

// driverHost is the SCM handler: it starts the child, reports Running, and
// on Stop or Shutdown kills the child and reports Stopped. The child has no
// console, so there is no gentler signal to send it than Kill; the driver's
// hardware handle is released by the OS.
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
			// The child ended on its own: report a failure so the SCM's
			// recovery options restart the service.
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
