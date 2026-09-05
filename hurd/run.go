package hurd

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// version is reported to clients and stamped by release builds with -ldflags -X.
var version = "v0.1.0"

// built pairs a configured device spec with the constructed driver.
type built struct {
	spec DeviceSpec
	dev  alpacadev.Device
	num  int // ASCOM device number on its port
}

// Main parses flags and runs the herd or the requested CLI command.
func Main() {
	cfgPath := flag.String("config", "",
		"server config path (default: ALPACAHURD_CONFIG, then current and platform config directories)")
	listDrivers := flag.Bool("drivers", false,
		"list compiled-in drivers and exit")
	example := flag.Bool("example", false,
		"print server settings, or a device entry with -example <driver>, and exit")
	check := flag.Bool("check", false,
		"check config without hardware access; only server-fatal errors exit nonzero")
	exampleDevices := flag.String("example-devices", "",
		"write disabled hardware driver templates to this directory, preserving existing files")
	launchInstance := flag.String("launch", "",
		"launch a device instance (its devices.d filename stem) through an installed driver binary")
	flag.Parse()

	switch {
	case *listDrivers:
		printDrivers(os.Stdout)
		return
	case *example:
		if err := printExample(os.Stdout, flag.Arg(0)); err != nil {
			log.Fatalf("alpacahurd: %v", err)
		}
		return
	case *exampleDevices != "":
		if err := writeExampleDevicesDir(os.Stdout, *exampleDevices); err != nil {
			log.Fatalf("alpacahurd: %v", err)
		}
		return
	}

	resolvedCfg, err := resolveConfigPath(*cfgPath)
	if err != nil {
		log.Fatalf("alpacahurd: %v", err)
	}
	cfg, err := LoadConfig(resolvedCfg)
	if err != nil {
		log.Fatalf("alpacahurd: %v", err)
	}

	if *launchInstance != "" {
		if err := launch(resolvedCfg, cfg, *launchInstance); err != nil {
			// EX_CONFIG prevents systemd from retrying a configuration error.
			log.Printf("alpacahurd: %v", err)
			os.Exit(78)
		}
		return
	}

	if *check {
		fmt.Printf("checking %s\n", resolvedCfg)
		// Per-device errors do not prevent the remaining devices from starting.
		if fatal, _ := checkConfig(os.Stdout, cfg); fatal > 0 {
			os.Exit(1)
		}
		return
	}

	serve(cfg, resolvedCfg)
}

// serve runs device servers, supervision, and discovery until SIGINT or SIGTERM.
func serve(cfg *Config, cfgPath string) {
	log.Printf("alpacahurd: config %s", cfgPath)

	var logger *log.Logger
	if cfg.Debug {
		logger = log.New(os.Stderr, "alpaca ", log.LstdFlags|log.Lmsgprefix)
	}

	listenAddrs, listenIfaces, err := resolveListen(cfg.Listen)
	if err != nil {
		log.Fatalf("alpacahurd: %v", err)
	}

	orch := &orchestrator{cfgPath: cfgPath, cfg: cfg, sup: platformSupervisor(cfgPath), servers: map[int]*alpacadev.Server{}, startedAt: time.Now(),
		listenAddrs: listenAddrs, logger: logger, byPort: map[int]*alpacadev.Server{}, nums: map[int]*deviceNumbers{}}
	var servers []*alpacadev.Server
	var devices []built
	byPort := orch.byPort
	nums := orch.nums
	var scanned []scanEntry
	var separate []DeviceSpec // entries the supervisor runs
	for _, spec := range cfg.Devices {
		if !spec.enabled() {
			log.Printf("alpacahurd: skipping %s (disabled)", spec.Driver)
			orch.rows = append(orch.rows, orchRow{spec: spec, res: resolveDriver(spec), port: spec.Port})
			continue
		}
		if spec.Port == 0 && spec.Instance == "" {
			// Only device-file entries can persist an automatically assigned port.
			log.Printf("alpacahurd: device %q: \"port\" is required for an inline entry; skipping it", spec.Driver)
			orch.rows = append(orch.rows, orchRow{spec: spec, res: resolveDriver(spec), skipped: `"port" is required for an inline entry`})
			continue
		}
		res := resolveDriver(spec)
		switch res.kind {
		case compiledIn:
		case installedBinary:
			if _, none := orch.sup.(noSupervisor); none {
				log.Printf("alpacahurd: %s: driver %q resolves to %s; no platform supervisor here, so the entry is skipped (run it by hand: alpacahurd -launch %s)", spec.Source, spec.Driver, res.exe, spec.Instance)
				orch.rows = append(orch.rows, orchRow{spec: spec, res: res, port: spec.Port, skipped: "separate binary; no supervisor on this host"})
				continue
			}
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, port: spec.Port})
			separate = append(separate, spec)
			continue
		default:
			log.Printf("alpacahurd: %s: driver %q is not compiled in and no binary was found; skipping the entry", spec.Source, spec.Driver)
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, skipped: "driver not compiled in and no binary found"})
			continue
		}
		subs, serr := subSpecs(spec)
		if serr != nil {
			log.Printf("alpacahurd: %s: device %q: %v; skipping the entry", spec.Source, spec.Driver, serr)
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, port: spec.Port, skipped: serr.Error()})
			continue
		}
		// Negative keys give scanning instances separate servers.
		key := spec.Port
		if key == 0 {
			key = -1 - len(scanned) // distinct negative keys, one per scanning entry
		}
		srv, shared := byPort[key]
		if !shared {
			// Separate scan windows avoid concurrent port selection races.
			srv = orch.newServer(spec.Port, len(scanned))
			byPort[key] = srv
			nums[key] = &deviceNumbers{}
			servers = append(servers, srv)
			if spec.Port == 0 {
				scanned = append(scanned, scanEntry{srv: srv, spec: spec})
				orch.scanCount = len(scanned)
			}
		}
		for _, sub := range subs {
			if !sub.enabled() {
				log.Printf("alpacahurd: skipping %s device %d (disabled)", spec.Instance, *sub.Block)
				continue
			}
			dev, num, err := registerDevice(srv, sub, nums[key])
			if err != nil {
				log.Printf("alpacahurd: %s: device %q: %v; skipping the entry", sub.Source, sub.Driver, err)
				orch.rows = append(orch.rows, orchRow{spec: sub, res: res, port: sub.Port, skipped: err.Error()})
				continue
			}
			b := built{spec: sub, dev: dev, num: num}
			orch.rows = append(orch.rows, orchRow{spec: sub, res: res, num: num, inProcess: true, deviceName: dev.Name(), devType: deviceTypeOf(sub, dev), srvKey: key})
			if err := srv.SetReloader(deviceTypeOf(sub, dev), num, reloaderFor(sub)); err != nil {
				log.Fatalf("alpacahurd: device %q: %v", sub.Driver, err)
			}
			orch.rows[len(orch.rows)-1].reloadable = true
			devices = append(devices, b)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	orch.ctx = ctx

	for _, spec := range separate {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := orch.sup.Install(sctx, spec.Instance); err != nil {
			log.Printf("alpacahurd: %s: install with %s: %v", spec.Instance, orch.sup.Name(), err)
		} else if err := orch.sup.Start(sctx, spec.Instance); err != nil {
			log.Printf("alpacahurd: %s: start with %s: %v", spec.Instance, orch.sup.Name(), err)
		} else {
			log.Printf("alpacahurd: %s (%s) started under %s", spec.Instance, spec.Driver, orch.sup.Name())
		}
		cancel()
	}

	if len(servers) == 0 && len(separate) == 0 {
		log.Printf("alpacahurd: no enabled devices; enable one on the orchestrator page (below) or in %s", devicesDirFor(cfgPath))
	}

	nameServers(orch)

	// Bind fixed ports before scanning so scanners cannot claim them.
	errc := make(chan error, len(servers))
	var ports []int
	failed := map[*alpacadev.Server]error{}
	startOne := func(s *alpacadev.Server) {
		go func() { errc <- s.Run(ctx) }()
		p, err := waitBound(ctx, []*alpacadev.Server{s}, errc)
		if err != nil {
			failed[s] = err
			return
		}
		ports = append(ports, p[0])
	}
	scanning := map[*alpacadev.Server]bool{}
	for _, e := range scanned {
		scanning[e.srv] = true
	}
	for _, s := range servers {
		if !scanning[s] {
			startOne(s)
		}
	}
	for _, s := range servers {
		if scanning[s] {
			startOne(s)
		}
	}
	if len(failed) > 0 {
		kept := servers[:0]
		for _, s := range servers {
			if _, bad := failed[s]; !bad {
				kept = append(kept, s)
			}
		}
		servers = kept
	}
	orch.mu.Lock()
	var bound []boundEntry
	for i := range orch.rows {
		if orch.rows[i].inProcess {
			if err, bad := failed[byPort[orch.rows[i].srvKey]]; bad {
				log.Printf("alpacahurd: %s: %v; its devices are not served", orch.rows[i].spec.Instance, err)
				orch.rows[i].inProcess, orch.rows[i].reloadable = false, false
				orch.rows[i].skipped = err.Error()
				continue
			}
			orch.rows[i].port = byPort[orch.rows[i].srvKey].Port()
			bound = append(bound, boundEntry{spec: orch.rows[i].spec, port: orch.rows[i].port})
			label := orch.rows[i].spec.Instance
			if label == "" {
				label = orch.rows[i].spec.Driver
			} else {
				label += " (" + orch.rows[i].spec.Driver + ")"
			}
			for _, line := range listenLines(orch.rows[i].port, listenAddrs) {
				log.Printf("alpacahurd: %s on %s as %s %d", label, line, orch.rows[i].devType, orch.rows[i].num)
			}
		}
	}
	for _, s := range servers {
		orch.servers[s.Port()] = s
	}
	for k, s := range byPort {
		if _, bad := failed[s]; bad {
			delete(byPort, k) // an enable from the page starts a fresh one
		}
	}
	orch.mu.Unlock()
	persistBoundPorts(bound)

	// Keep the setup page available even when no devices are running.
	if sp := cfg.setupPort(); sp != 0 {
		pageCfg := func(port, scanBase int) alpacadev.Config {
			return alpacadev.Config{
				AlpacaPort: port, PortScanBase: scanBase, PortScanLimit: 20,
				Hosts:      listenAddrs,
				Discovery:  alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
				ServerName: "alpacahurd", Manufacturer: "mikefsq", ManufacturerVersion: version,
				Logger: logger, ConfigPath: cfgPath,
				SetupPages: orch.setupPages(), SetupHome: orch,
			}
		}
		pageErrc := make(chan error, 2)
		pageSrv := alpacadev.New(pageCfg(sp, 0))
		go func() { pageErrc <- pageSrv.Run(ctx) }()
		p, err := waitBound(ctx, []*alpacadev.Server{pageSrv}, pageErrc)
		if err != nil {
			log.Printf("alpacahurd: setup page on :%d: %v; scanning from %d", sp, err, sp+1)
			pageSrv = alpacadev.New(pageCfg(0, sp+1))
			go func() { pageErrc <- pageSrv.Run(ctx) }()
			if p, err = waitBound(ctx, []*alpacadev.Server{pageSrv}, pageErrc); err != nil {
				log.Fatalf("alpacahurd: setup page: %v", err)
			}
		}
		orch.setPagePort(p[0])
		for _, line := range listenLines(p[0], listenAddrs) {
			log.Printf("alpacahurd: orchestrator page on %s", line)
		}
	}

	if !strings.EqualFold(cfg.Discovery, "off") {
		resp := newResponder(ports, orch.noteRegistration)
		if err := runDiscovery(ctx, resp, cfg.ipv6Enabled(), listenIfaces); err != nil {
			log.Fatalf("alpacahurd: discovery: %v", err)
		}
		orch.mu.Lock()
		orch.resp = resp
		orch.mu.Unlock()
		log.Printf("alpacahurd: discovery responder on :%d for %d port(s), plus registrations", discoveryPort, len(ports))
	}

	onReloadSignal(ctx, func() {
		log.Printf("alpacahurd: reload requested")
		for _, s := range servers {
			if err := s.ReloadAll(ctx); err != nil {
				log.Printf("alpacahurd: reload: %v", err)
			}
		}
	})

	orch.mu.Lock()
	for i := range orch.rows {
		row := &orch.rows[i]
		if !row.inProcess {
			continue
		}
		if drv, ok := registry.Lookup(row.spec.Driver); ok {
			row.stopFrontEnd = wireFrontEnd(ctx, drv, byPort[row.srvKey], row.num, row.spec, listenAddrs)
		}
	}
	orch.mu.Unlock()

	log.Printf("alpacahurd: serving %d device(s) on %d port(s) (Ctrl-C to stop)", len(devices), len(servers))

	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			log.Fatalf("alpacahurd: %v", err)
		}
	}
	log.Printf("alpacahurd: shut down")
}

// nameServers uses the device identity for servers hosting exactly one device.
func nameServers(orch *orchestrator) {
	count := map[int]int{}
	for _, row := range orch.rows {
		if row.inProcess {
			count[row.srvKey]++
		}
	}
	for _, row := range orch.rows {
		if !row.inProcess || count[row.srvKey] != 1 {
			continue
		}
		if srv := orch.byPort[row.srvKey]; srv != nil {
			srv.SetIdentity(row.deviceName, "", driverVersion(row.spec.Driver))
		}
	}
}

// driverVersion returns the driver module version, falling back to the hurd version.
func driverVersion(driver string) string {
	if drv, ok := registry.Lookup(driver); ok {
		if v := drv.ModuleVersion(); v != "" {
			return v
		}
	}
	return version
}
