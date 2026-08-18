package hurd

import (
	"context"
	"fmt"
	"log"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// newServer builds one device server the way serve does: on port when it is
// set, else scanning its own window above portScanBase (the scanIndex-th
// window, so concurrent scanners never race for one port), with the setup
// redirect, the listen addresses, the request logger, and per-device settings
// files under the state directory.
func (o *orchestrator) newServer(port, scanIndex int) *alpacadev.Server {
	return alpacadev.New(alpacadev.Config{
		AlpacaPort:          port,
		PortScanBase:        portScanBase + scanIndex*portScanSpan,
		PortScanLimit:       portScanSpan,
		SetupPages:          o.redirectPages(),
		Hosts:               o.listenAddrs,
		Discovery:           alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
		ServerName:          "alpacahurd",
		Manufacturer:        "mikefsq",
		ManufacturerVersion: version,
		Logger:              o.logger,
		ConfigPath:          o.cfgPath,
		// Setup-page changes persist under the state directory, one file
		// per device (see Server.SettingsPath); the config file's own keys
		// render locked, so persistence never overrides an admin value.
		Settings: alpacadev.NewFileStore(),
	})
}

// setEnabled is the orchestrator page's enable and disable for a devices.d
// entry, in either layout, without a restart. The switch is written to the
// entry's state file first, so the next start agrees with the page; then the
// device is acted on: a compiled-in driver is constructed and registered on
// its port's server (a new server is started for a new port), or unregistered
// with its hardware closed; a separate binary is started or stopped through
// the supervisor, and enabled or disabled at boot there. The message says
// what happened.
func (o *orchestrator) setEnabled(ctx context.Context, inst string, on bool) (string, error) {
	if inst == "" || inst == "(inline)" {
		return "", fmt.Errorf("only a devices.d entry can be switched; an inline entry is edited in hurd.json")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var row *orchRow
	for i := range o.rows {
		if o.rows[i].spec.Instance == inst {
			row = &o.rows[i]
			break
		}
	}
	if row == nil {
		return "", fmt.Errorf("no device %q", inst)
	}
	if err := writeStateEnable(inst, on); err != nil {
		return "", fmt.Errorf("record the switch: %w", err)
	}
	cur, err := loadDeviceFile(row.spec.Source, stateDevicesDir())
	if err != nil {
		return "", err
	}
	row.spec = cur
	row.res = resolveDriver(cur)
	row.skipped = ""

	if !on {
		switch {
		case row.inProcess:
			srv := o.byPort[row.srvKey]
			if err := srv.Unregister(row.devType, row.num); err != nil {
				return "", err
			}
			if n := o.nums[row.srvKey]; n != nil {
				n.release(row.devType, row.num)
			}
			log.Printf("alpacahurd: %s disabled from the page; %s %d on :%d closed", inst, row.devType, row.num, row.port)
			row.inProcess, row.reloadable, row.num, row.deviceName = false, false, 0, ""
			return fmt.Sprintf("%s disabled: hardware closed and the device removed from port %d", inst, row.port), nil
		case row.res.kind == installedBinary:
			if _, none := o.sup.(noSupervisor); none {
				return fmt.Sprintf("%s disabled in its state file; no supervisor here to stop it", inst), nil
			}
			var errs []string
			if err := o.sup.Stop(ctx, inst); err != nil {
				errs = append(errs, "stop: "+err.Error())
			}
			if err := o.sup.Disable(ctx, inst); err != nil {
				errs = append(errs, "disable at boot: "+err.Error())
			}
			if len(errs) > 0 {
				return "", fmt.Errorf("%s disabled in its state file, but %s: %v", inst, o.sup.Name(), errs)
			}
			return fmt.Sprintf("%s disabled: stopped under %s and off at boot", inst, o.sup.Name()), nil
		}
		return fmt.Sprintf("%s disabled", inst), nil
	}

	// Enable.
	if row.inProcess {
		return fmt.Sprintf("%s is already running", inst), nil
	}
	switch row.res.kind {
	case compiledIn:
		msg, err := o.startInProcess(row)
		if err != nil {
			// Nothing runs, so the switch goes back to off: an entry recorded
			// enabled that cannot be built would fail the next start.
			_ = writeStateEnable(inst, false)
			if again, lerr := loadDeviceFile(row.spec.Source, stateDevicesDir()); lerr == nil {
				row.spec = again
			}
			row.skipped = err.Error()
			return "", fmt.Errorf("%s not enabled: %w", inst, err)
		}
		return msg, nil
	case installedBinary:
		if _, none := o.sup.(noSupervisor); none {
			return fmt.Sprintf("%s enabled in its state file; no supervisor here to start it (alpacahurd -launch %s runs it by hand)", inst, inst), nil
		}
		if err := o.sup.Install(ctx, inst); err != nil {
			return "", err
		}
		if err := o.sup.Enable(ctx, inst); err != nil {
			return "", err
		}
		if err := o.sup.Start(ctx, inst); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s enabled: started under %s and on at boot", inst, o.sup.Name()), nil
	}
	_ = writeStateEnable(inst, false)
	row.skipped = "driver not compiled in and no binary found"
	return "", fmt.Errorf("%s not enabled: its driver %q is not compiled in and no binary was found", inst, cur.Driver)
}

// startInProcess constructs row's device and serves it: on the running server
// for its port when one exists, else on a new server started now (a set port
// or a fresh scan window), whose port is recorded in the state file and added
// to the discovery responder. Called with o.mu held.
func (o *orchestrator) startInProcess(row *orchRow) (string, error) {
	if o.ctx == nil || o.byPort == nil {
		return "", fmt.Errorf("the orchestrator is not serving; a restart is needed")
	}
	spec := row.spec
	drv, dev, err := buildDevice(spec)
	if err != nil {
		return "", err
	}
	key := spec.Port
	srv, ok := o.byPort[key]
	if key == 0 || !ok {
		if key == 0 {
			key = -1 - o.scanCount
			srv = o.newServer(0, o.scanCount)
			o.scanCount++
		} else {
			srv = o.newServer(spec.Port, 0)
		}
		// New server for this one device: it carries the device's identity.
		srv.SetIdentity(dev.Name(), "", driverVersion(spec.Driver))
		errc := make(chan error, 1)
		go func() { errc <- srv.Run(o.ctx) }()
		if _, err := waitBound(o.ctx, []*alpacadev.Server{srv}, errc); err != nil {
			return "", fmt.Errorf("start a server for %s: %w", spec.Instance, err)
		}
		o.byPort[key] = srv
		o.nums[key] = &deviceNumbers{}
		o.servers[srv.Port()] = srv
		if o.resp != nil {
			o.resp.addPort(srv.Port())
		}
	}
	nums := o.nums[key]
	num, err := nums.assign(spec, drv.Type)
	if err != nil {
		return "", err
	}
	if err := srv.Register(drv.Type, num, dev); err != nil {
		nums.release(drv.Type, num)
		return "", err
	}
	if err := attachSetupForm(srv, drv, dev, num, spec); err != nil {
		return "", err
	}
	note := ""
	if !heldByFrontEnd(o.cfg, spec, dev) {
		_ = srv.SetReloader(drv.Type, num, reloaderFor(spec, nil))
		row.reloadable = true
	} else {
		note = "; its INDI or LX200 front-end attaches at the next restart"
	}
	row.inProcess, row.num, row.port, row.srvKey = true, num, srv.Port(), key
	row.deviceName, row.devType = dev.Name(), drv.Type
	persistBoundPorts([]boundEntry{{spec: spec, port: srv.Port()}})
	log.Printf("alpacahurd: %s enabled from the page; %s %d on :%d", spec.Instance, drv.Type, num, srv.Port())
	return fmt.Sprintf("%s enabled: serving as %s %d on port %d%s", spec.Instance, drv.Type, num, srv.Port(), note), nil
}

// enableTimeout bounds a page enable or disable, hardware open included.
const enableTimeout = 30 * time.Second
