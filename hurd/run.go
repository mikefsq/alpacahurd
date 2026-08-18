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
	indiccd "github.com/mikefsq/goindi/ccd"
	indimount "github.com/mikefsq/goindi/mount"
	indiserver "github.com/mikefsq/goindi/server"
	"github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/bridge"
)

const version = "v0.1.0"

// liveMounter is implemented by mount drivers; LiveMount returns the connected
// lx200.Mount that the LX200 bridge and INDI server consume.
type liveMounter interface {
	LiveMount() (lx200.Mount, error)
}

// liveCamera is implemented by camera drivers that drive the INDI CCD device;
// LiveCamera returns the frame source.
type liveCamera interface {
	LiveCamera() (indiccd.Camera, error)
}

// opticsConfigurable is implemented by mount drivers that accept a shared optics
// holder, so the INDI front-end reports what an Alpaca setoptics Action sets.
type opticsConfigurable interface {
	UseOptics(alpacadev.OpticsStore)
}

// built pairs a configured device spec with the constructed driver, so the extra
// front-ends (INDI hub, LX200 bridge) can be wired onto the same device object.
type built struct {
	spec   DeviceSpec
	dev    alpacadev.Device
	num    int           // ASCOM device number on its port
	optics *opticsHolder // shared optics holder, when the driver accepts one
}

// Main is the alpacahurd entry point: it parses flags and either serves the
// configured herd or runs one of the introspection modes (-drivers, -example,
// -check) and exits.
func Main() {
	cfgPath := flag.String("config", "",
		"path to the device config JSON file (default: search ./hurd.json, "+
			"$XDG_CONFIG_HOME/alpacahurd/hurd.json, /etc/alpacahurd/hurd.json; or $ALPACAHURD_CONFIG)")
	listDrivers := flag.Bool("drivers", false,
		"list the drivers compiled into this binary and exit")
	example := flag.Bool("example", false,
		"print a starter device config assembled from every compiled-in driver and exit; "+
			"name a driver as an argument (alpacahurd -example astrocam) for just its entry")
	check := flag.Bool("check", false,
		"load the config and construct every enabled device (no hardware is touched), "+
			"report problems, and exit non-zero on any error")
	exampleDevices := flag.String("example-devices", "",
		"write one disabled example device file per compiled-in driver into this directory "+
			"(the devices.d beside hurd.json) and exit; existing files are kept")
	launchInstance := flag.String("launch", "",
		"run one device (a devices.d file's stem) as a separate process for the platform supervisor: "+
			"resolve its driver to the installed binary and replace this process with it")
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
			log.Fatalf("alpacahurd: %v", err)
		}
		return
	}

	if *check {
		fmt.Printf("checking %s\n", resolvedCfg)
		if errs := checkConfig(os.Stdout, cfg); errs > 0 {
			os.Exit(1)
		}
		return
	}

	serve(cfg, resolvedCfg)
}

// serve runs the whole herd until SIGINT/SIGTERM: one Alpaca server per enabled
// device, the shared discovery responder, and the INDI/LX200 front-ends.
func serve(cfg *Config, cfgPath string) {
	log.Printf("alpacahurd: config %s", cfgPath)

	var logger *log.Logger
	if cfg.Debug {
		// One line per Alpaca request (client addr, method, URI, status, duration).
		logger = log.New(os.Stderr, "alpaca ", log.LstdFlags|log.Lmsgprefix)
	}

	// Resolve "listen" into concrete bind addresses and the interfaces they live on.
	// Empty means bind every interface.
	listenAddrs, listenIfaces, err := resolveListen(cfg.Listen)
	if err != nil {
		log.Fatalf("alpacahurd: %v", err)
	}

	// One Alpaca server per distinct port, shared by every entry naming it, with
	// device numbers handed out per port (deviceNumbers). A port to itself is the
	// common case and still yields device 0. The maps live on the orchestrator,
	// which adds to them when a device is enabled from the page.
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
			// Listed on the orchestrator page with its file and driver, so a
			// device added disabled (the page's add form writes them so) is
			// visible before it is enabled; nothing is built for it.
			log.Printf("alpacahurd: skipping %s (disabled)", spec.Driver)
			orch.rows = append(orch.rows, orchRow{spec: spec, res: resolveDriver(spec), port: spec.Port})
			continue
		}
		if spec.Port == 0 && spec.Instance == "" {
			// An inline entry names its port. A devices.d entry may leave it
			// unset, in which case its server scans from portScanBase and the
			// bound port is persisted to the entry's state file (see
			// persistBoundPorts), so it is stable from the next start.
			log.Printf("alpacahurd: device %q: \"port\" is required for an inline entry; skipping it", spec.Driver)
			orch.rows = append(orch.rows, orchRow{spec: spec, res: resolveDriver(spec), skipped: `"port" is required for an inline entry`})
			continue
		}
		res := resolveDriver(spec)
		switch res.kind {
		case compiledIn:
		case installedBinary:
			// The driver is a separate binary: the platform supervisor runs it
			// as `alpacahurd -launch <instance>`. Without a supervisor (a
			// hand run, or a platform with none) it is reported and skipped,
			// so a mixed deployment still starts its compiled-in devices.
			if _, none := orch.sup.(noSupervisor); none {
				log.Printf("alpacahurd: %s: driver %q resolves to %s; no platform supervisor here, so the entry is skipped (run it by hand: alpacahurd -launch %s)", spec.Source, spec.Driver, res.exe, spec.Instance)
				orch.rows = append(orch.rows, orchRow{spec: spec, res: res, port: spec.Port, skipped: "separate binary; no supervisor on this host"})
				continue
			}
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, port: spec.Port})
			separate = append(separate, spec)
			continue
		default:
			// An entry whose driver is neither compiled in nor installed. A
			// removed driver package leaves its devices.d fragment behind
			// (dpkg keeps a conffile on remove), and a typo in an inline
			// entry is the same shape; either is reported and skipped rather
			// than fatal, so the rest of the herd serves.
			log.Printf("alpacahurd: %s: driver %q is not compiled in and no binary was found; skipping the entry", spec.Source, spec.Driver)
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, skipped: "driver not compiled in and no binary found"})
			continue
		}
		// Entries naming a port share a server per port; an entry with no port
		// gets its own server that scans, keyed by instance so it never shares.
		key := spec.Port
		if key == 0 {
			key = -1 - len(scanned) // distinct negative keys, one per scanning entry
		}
		srv, shared := byPort[key]
		if !shared {
			// Each scanning server gets its own window above portScanBase, so
			// five entries scanning at once cannot race for the same port: the
			// bind-as-probe guarantee holds within one server, not across
			// several started together against one base.
			srv = orch.newServer(spec.Port, len(scanned))
			byPort[key] = srv
			nums[key] = &deviceNumbers{}
			servers = append(servers, srv)
			if spec.Port == 0 {
				scanned = append(scanned, scanEntry{srv: srv, spec: spec})
				orch.scanCount = len(scanned)
			}
		}
		dev, num, err := registerDevice(srv, spec, nums[key])
		if err != nil {
			// A device that cannot be built (a missing key, a bad value) is
			// reported and skipped, not fatal: the rest of the herd serves,
			// and the page shows the error beside a disable switch.
			log.Printf("alpacahurd: %s: device %q: %v; skipping the entry", spec.Source, spec.Driver, err)
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, port: spec.Port, skipped: err.Error()})
			continue
		}
		b := built{spec: spec, dev: dev, num: num}
		orch.rows = append(orch.rows, orchRow{spec: spec, res: res, num: num, inProcess: true, deviceName: dev.Name(), devType: deviceTypeOf(spec, dev), srvKey: key})
		// Inject a shared optics holder so the INDI front-end's TELESCOPE_INFO reports
		// whatever an Alpaca setoptics Action sets.
		if oc, ok := dev.(opticsConfigurable); ok {
			b.optics = newOpticsHolder(spec.Aperture, spec.ApertureArea, spec.FocalLength,
				spec.GuiderAperture, spec.GuiderFocalLength)
			oc.UseOptics(b.optics)
		}
		// Reload in place, from the setup page or the orchestrator page,
		// unless an INDI or LX200 front-end holds the device object: those
		// wrap it at start and would keep driving the old one.
		if !heldByFrontEnd(cfg, spec, dev) {
			if err := srv.SetReloader(deviceTypeOf(spec, dev), num, reloaderFor(spec, b.optics)); err != nil {
				log.Fatalf("alpacahurd: device %q: %v", spec.Driver, err)
			}
			orch.rows[len(orch.rows)-1].reloadable = true
		}
		devices = append(devices, b)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	orch.ctx = ctx

	// Separate binaries: install each with the supervisor and start it. The
	// device is a peer under the supervisor, so a failure here is logged and
	// the page shows it; the in-process devices are unaffected.
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
		// Not fatal: a freshly-installed box has a config with every device
		// disabled. Stay up (so systemd shows the service healthy) with the
		// orchestrator page serving, since that page is where a device gets
		// enabled; the message says so.
		log.Printf("alpacahurd: no enabled devices; enable one on the orchestrator page (below) or in %s", devicesDirFor(cfgPath))
	}

	// A server hosting one device is that device to a client: it takes the
	// device's name and its driver's module version as its identity, so the
	// port's setup page and /management/v1/description say "10Micron GM"
	// rather than "alpacahurd". A shared port stays alpacahurd's.
	nameServers(orch)

	// Start the servers, so every port is known before discovery. Servers on
	// a set port go first and bind before any scanner starts, so a scanner
	// whose window covers a set port finds it taken and moves on rather than
	// racing the pinned server for it. A server that cannot bind (its port
	// held by another process) is dropped with its devices reported on the
	// page, not fatal: the rest of the herd serves.
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

	// The orchestrator page on its own port, so it has one address whatever the
	// layout and needs no device port to exist. It is a bare server with no
	// devices, only the page; / and /setup land on it. If the preferred port is
	// taken, a scan from the next port up finds a free one and the log says where.
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
		// The responder answers for the in-process ports and for every device
		// that registers with it: a separate binary on this host directly, one
		// on another host through the relay endpoint.
		resp := newResponder(ports, orch.noteRegistration)
		if err := runDiscovery(ctx, resp, cfg.ipv6Enabled(), listenIfaces); err != nil {
			log.Fatalf("alpacahurd: discovery: %v", err)
		}
		orch.mu.Lock()
		orch.resp = resp
		orch.mu.Unlock()
		log.Printf("alpacahurd: discovery responder on :%d for %d port(s), plus registrations", discoveryPort, len(ports))
	}

	// SIGHUP reloads every in-process device that can be: configuration
	// re-read, hardware closed and reopened, ports kept.
	onReloadSignal(ctx, func() {
		log.Printf("alpacahurd: reload requested")
		for _, s := range servers {
			if err := s.ReloadAll(ctx); err != nil {
				log.Printf("alpacahurd: reload: %v", err)
			}
		}
	})

	startINDI(ctx, cfg, devices, listenAddrs)
	startBridges(ctx, cfg, devices, listenAddrs)
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

// startINDI hosts a single in-process INDI server on one port (default 7624) with
// every INDI-capable device, multiplexed by device name. Each device drives the same
// object the Alpaca server does. INDI has no discovery, so device names must be
// unique; a collision is a startup error.
func startINDI(ctx context.Context, cfg *Config, devices []built, listenAddrs []string) {
	if !cfg.Indi.Enable {
		return
	}
	indiAddrs := listenAddrsFor(cfg.Indi.port(), listenAddrs)
	hub := indiserver.New(indiAddrs[0],
		indiserver.WithLogger(log.Printf),
		indiserver.WithListenAddrs(indiAddrs...),
		indiserver.WithDebug(cfg.Debug))
	added := 0
	for _, b := range devices {
		if !b.spec.indiEnabled() {
			continue
		}
		name := indiName(b.spec, b.num)
		var dev indiserver.Device
		switch {
		case isLiveMounter(b.dev):
			var opts []indimount.Option
			if b.optics != nil {
				opts = append(opts, indimount.WithOptics(b.optics))
			}
			rate := b.spec.GuideRate
			if rate == 0 {
				rate = 0.5
			}
			opts = append(opts, indimount.WithGuideRate(rate))
			dev = indimount.New(name, b.dev.(liveMounter).LiveMount, opts...)
		case isLiveCamera(b.dev):
			dev = indiccd.New(name, b.dev.(liveCamera).LiveCamera)
		default:
			continue // not an INDI-capable device
		}
		if err := hub.AddDevice(dev); err != nil {
			log.Fatalf("alpacahurd: indi: %v", err)
		}
		added++
	}
	if added == 0 {
		return
	}
	go func() {
		log.Printf("alpacahurd: INDI server on %v for %d device(s)", indiAddrs, added)
		if err := hub.Serve(ctx); err != nil && ctx.Err() == nil {
			log.Printf("alpacahurd: indi: %v", err)
		}
	}()
}

// startBridges serves a Meade-LX200 TCP server (Stellarium/SkySafari) per mount.
// LX200 can't multiplex, so each mount needs its own port: when the top-level
// "lx200" block is enabled every mount gets one from BasePort upward; a mount can pin
// its own with "lx200Port", which also enables it on its own.
func startBridges(ctx context.Context, cfg *Config, devices []built, listenAddrs []string) {
	next := cfg.LX200.basePort()
	for _, b := range devices {
		lm, ok := b.dev.(liveMounter)
		if !ok {
			if b.spec.LX200Port != 0 {
				log.Fatalf("alpacahurd: %q sets \"lx200Port\" but is not a mount", b.spec.Driver)
			}
			continue
		}
		port := b.spec.LX200Port // explicit per-mount override
		if port == 0 {
			if !cfg.LX200.Enable {
				continue
			}
			port = next
			next++
		}
		opts := []bridge.Option{bridge.WithLogger(log.Printf)}
		if cfg.LX200.ReadOnlySite {
			opts = append(opts, bridge.WithReadOnlySite())
		}
		// Stateless over LiveMount, so bind one server per listen address.
		for _, addr := range listenAddrsFor(port, listenAddrs) {
			srv := bridge.New(addr, lm.LiveMount, opts...)
			a, driver := addr, b.spec.Driver
			go func() {
				log.Printf("alpacahurd: LX200 bridge on %s for %s", a, driver)
				if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
					log.Printf("alpacahurd: lx200 bridge: %v", err)
				}
			}()
		}
	}
}

// heldByFrontEnd reports whether the INDI hub or an LX200 bridge will wrap dev
// for the process lifetime, which rules out reloading it in place.
func heldByFrontEnd(cfg *Config, spec DeviceSpec, dev alpacadev.Device) bool {
	if cfg.Indi.Enable && spec.indiEnabled() && (isLiveMounter(dev) || isLiveCamera(dev)) {
		return true
	}
	if isLiveMounter(dev) && (spec.LX200Port != 0 || cfg.LX200.Enable) {
		return true
	}
	return false
}

func isLiveMounter(d alpacadev.Device) bool { _, ok := d.(liveMounter); return ok }
func isLiveCamera(d alpacadev.Device) bool  { _, ok := d.(liveCamera); return ok }

// indiName is the INDI device id clients select by: the configured name, or a
// fallback derived from the driver and its Alpaca address. Device 0 is named for
// the port alone, so sharing a port never renames an existing device; the
// entries beside it take the device number too. Give INDI devices an explicit
// "name" — that is what PHD2 shows.
func indiName(spec DeviceSpec, num int) string {
	if spec.Name != "" {
		return spec.Name
	}
	if num == 0 {
		return fmt.Sprintf("%s-%d", spec.Driver, spec.Port)
	}
	return fmt.Sprintf("%s-%d-%d", spec.Driver, spec.Port, num)
}

// nameServers gives each server hosting exactly one in-process device that
// device's name and driver version as its identity; see serve.
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

// driverVersion is the module version of a compiled-in driver, from the
// binary's build info, falling back to the hurd's own version when the build
// info does not name it (a workspace build reports "(devel)").
func driverVersion(driver string) string {
	if drv, ok := registry.Lookup(driver); ok {
		if v := drv.ModuleVersion(); v != "" {
			return v
		}
	}
	return version
}
