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

	alpacadev "github.com/mikefsq/goalpaca/server"
	indiccd "github.com/mikefsq/goindi/ccd"
	indimount "github.com/mikefsq/goindi/mount"
	indiserver "github.com/mikefsq/goindi/server"
	"github.com/mikefsq/lx200"
	"github.com/mikefsq/lx200/bridge"
)

const version = "0.1.0"

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
			"name a driver as an argument (alpacahurd -example asicam) for just its entry")
	check := flag.Bool("check", false,
		"load the config and construct every enabled device (no hardware is touched), "+
			"report problems, and exit non-zero on any error")
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
	}

	resolvedCfg, err := resolveConfigPath(*cfgPath)
	if err != nil {
		log.Fatalf("alpacahurd: %v", err)
	}
	cfg, err := LoadConfig(resolvedCfg)
	if err != nil {
		log.Fatalf("alpacahurd: %v", err)
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

	var servers []*alpacadev.Server
	var ports []int
	var devices []built
	for _, spec := range cfg.Devices {
		if !spec.enabled() {
			log.Printf("alpacahurd: skipping %s (disabled)", spec.Driver)
			continue
		}
		if spec.Port == 0 {
			log.Fatalf("alpacahurd: device %q: \"port\" is required", spec.Driver)
		}
		srv := alpacadev.New(alpacadev.Config{
			AlpacaPort:          spec.Port,
			Hosts:               listenAddrs,
			Discovery:           alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
			ServerName:          "alpacahurd",
			Manufacturer:        "mikefsq",
			ManufacturerVersion: version,
			Logger:              logger,
		})
		// Each device gets its own per-port Alpaca server, so ASCOM device numbers
		// are per-server: every device is number 0 of its type on its port.
		dev, err := registerDevice(srv, spec)
		if err != nil {
			log.Fatalf("alpacahurd: device %q: %v", spec.Driver, err)
		}
		b := built{spec: spec, dev: dev}
		// Inject a shared optics holder so the INDI front-end's TELESCOPE_INFO reports
		// whatever an Alpaca setoptics Action sets.
		if oc, ok := dev.(opticsConfigurable); ok {
			b.optics = newOpticsHolder(spec.Aperture, spec.ApertureArea, spec.FocalLength,
				spec.GuiderAperture, spec.GuiderFocalLength)
			oc.UseOptics(b.optics)
		}
		servers = append(servers, srv)
		ports = append(ports, spec.Port)
		devices = append(devices, b)
		for _, line := range listenLines(spec.Port, listenAddrs) {
			log.Printf("alpacahurd: %s on %s", spec.Driver, line)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if len(servers) == 0 {
		// Not fatal: a freshly-installed box has a config with every device
		// disabled. Stay up (so systemd shows the service healthy) and say
		// clearly what to do next.
		log.Printf("alpacahurd: no enabled devices in %s — edit it and restart", cfgPath)
		<-ctx.Done()
		log.Printf("alpacahurd: shut down")
		return
	}

	if !strings.EqualFold(cfg.Discovery, "off") {
		if err := runDiscovery(ctx, ports, cfg.ipv6Enabled(), listenIfaces); err != nil {
			log.Fatalf("alpacahurd: discovery: %v", err)
		}
		log.Printf("alpacahurd: discovery responder on :%d for %d port(s)", discoveryPort, len(ports))
	}

	startINDI(ctx, cfg, devices, listenAddrs)
	startBridges(ctx, cfg, devices, listenAddrs)

	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func(s *alpacadev.Server) { errc <- s.Run(ctx) }(s)
	}
	log.Printf("alpacahurd: serving %d device(s) (Ctrl-C to stop)", len(servers))

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
		name := indiName(b.spec)
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

func isLiveMounter(d alpacadev.Device) bool { _, ok := d.(liveMounter); return ok }
func isLiveCamera(d alpacadev.Device) bool  { _, ok := d.(liveCamera); return ok }

// indiName is the INDI device id clients select by: the configured name, or a
// fallback derived from the driver and its Alpaca port (which is unique per
// device). Give INDI devices an explicit "name" — that is what PHD2 shows.
func indiName(spec DeviceSpec) string {
	if spec.Name != "" {
		return spec.Name
	}
	return fmt.Sprintf("%s-%d", spec.Driver, spec.Port)
}
