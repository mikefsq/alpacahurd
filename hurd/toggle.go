package hurd

import (
	"context"
	"fmt"
	"log"
	"strings"
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
	// A multi-device entry (subSpecs) has one row per block, sharing the
	// instance, the file, and the switch; collect them all.
	var idxs []int
	for i := range o.rows {
		if o.rows[i].spec.Instance == inst {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) == 0 {
		return "", fmt.Errorf("no device %q", inst)
	}
	source := o.rows[idxs[0]].spec.Source
	if err := writeStateEnable(inst, on); err != nil {
		return "", fmt.Errorf("record the switch: %w", err)
	}
	cur, err := loadDeviceFile(source, stateDevicesDir())
	if err != nil {
		return "", err
	}
	res := resolveDriver(cur)

	if !on {
		closed, port := 0, 0
		for _, i := range idxs {
			row := &o.rows[i]
			if !row.inProcess {
				continue
			}
			// The front-end goes with the device: cancel its context before
			// the registration is removed.
			if row.stopFrontEnd != nil {
				row.stopFrontEnd()
				row.stopFrontEnd = nil
			}
			srv := o.byPort[row.srvKey]
			if err := srv.Unregister(row.devType, row.num); err != nil {
				return "", err
			}
			if n := o.nums[row.srvKey]; n != nil {
				n.release(row.devType, row.num)
			}
			log.Printf("alpacahurd: %s disabled from the page; %s %d on :%d closed", inst, row.devType, row.num, row.port)
			port = row.port
			closed++
		}
		if closed > 0 {
			// The entry collapses back to one disabled row.
			o.replaceRows(idxs, []orchRow{{spec: cur, res: res, port: cur.Port}})
			if closed == 1 {
				return fmt.Sprintf("%s disabled: hardware closed and the device removed from port %d", inst, port), nil
			}
			return fmt.Sprintf("%s disabled: hardware closed and %d devices removed from port %d", inst, closed, port), nil
		}
		o.replaceRows(idxs, []orchRow{{spec: cur, res: res, port: cur.Port}})
		if res.kind == installedBinary {
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
	for _, i := range idxs {
		if o.rows[i].inProcess {
			return fmt.Sprintf("%s is already running", inst), nil
		}
	}
	switch res.kind {
	case compiledIn:
		subs, err := subSpecs(cur)
		if err != nil {
			_ = writeStateEnable(inst, false)
			o.replaceRows(idxs, []orchRow{{spec: cur, res: res, port: cur.Port, skipped: err.Error()}})
			return "", fmt.Errorf("%s not enabled: %w", inst, err)
		}
		var newRows []orchRow
		var msgs []string
		for _, sub := range subs {
			if !sub.enabled() {
				continue // a disabled block: no device at its number
			}
			row := orchRow{spec: sub, res: res, port: sub.Port}
			msg, err := o.startInProcess(&row)
			if err != nil {
				// The switch goes back to off: an entry recorded enabled that
				// cannot be built would fail the next start. Devices started
				// for earlier blocks keep serving until then.
				_ = writeStateEnable(inst, false)
				row.skipped = err.Error()
				o.replaceRows(idxs, append(newRows, row))
				return "", fmt.Errorf("%s not enabled: %w", inst, err)
			}
			newRows = append(newRows, row)
			msgs = append(msgs, msg)
		}
		if len(newRows) == 0 {
			o.replaceRows(idxs, []orchRow{{spec: cur, res: res, port: cur.Port, skipped: "every device block is disabled"}})
			return fmt.Sprintf("%s enabled, but every device block in its file is disabled", inst), nil
		}
		o.replaceRows(idxs, newRows)
		return strings.Join(msgs, "; "), nil
	case installedBinary:
		o.replaceRows(idxs, []orchRow{{spec: cur, res: res, port: cur.Port}})
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
	o.replaceRows(idxs, []orchRow{{spec: cur, res: res, port: cur.Port, skipped: "driver not compiled in and no binary found"}})
	return "", fmt.Errorf("%s not enabled: its driver %q is not compiled in and no binary was found", inst, cur.Driver)
}

// replaceRows swaps the rows at idxs (ascending, as collected by instance) for
// rows, keeping the entry's place in the table. Called with o.mu held.
func (o *orchestrator) replaceRows(idxs []int, rows []orchRow) {
	drop := make(map[int]bool, len(idxs))
	for _, i := range idxs {
		drop[i] = true
	}
	out := make([]orchRow, 0, len(o.rows)-len(idxs)+len(rows))
	for i := range o.rows {
		if i == idxs[0] {
			out = append(out, rows...)
		}
		if !drop[i] {
			out = append(out, o.rows[i])
		}
	}
	o.rows = out
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
	_ = srv.SetReloader(drv.Type, num, reloaderFor(spec))
	row.reloadable = true
	// The driver's front-end, as serve wires it: a fresh one per enable,
	// stopped again by the disable path.
	row.stopFrontEnd = wireFrontEnd(o.ctx, drv, srv, num, spec, o.listenAddrs)
	row.inProcess, row.num, row.port, row.srvKey = true, num, srv.Port(), key
	row.deviceName, row.devType = dev.Name(), drv.Type
	persistBoundPorts([]boundEntry{{spec: spec, port: srv.Port()}})
	log.Printf("alpacahurd: %s enabled from the page; %s %d on :%d", spec.Instance, drv.Type, num, srv.Port())
	return fmt.Sprintf("%s enabled: serving as %s %d on port %d", spec.Instance, drv.Type, num, srv.Port()), nil
}

// enableTimeout bounds a page enable or disable, hardware open included.
const enableTimeout = 30 * time.Second
