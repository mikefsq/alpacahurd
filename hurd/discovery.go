package hurd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const (
	discoveryPort    = 32227
	discoveryToken   = "alpacadiscovery" // probe prefix; a version digit follows
	discoveryV6Group = "ff12::00a1:9aca" // IPv6 discovery multicast group
)

// responder answers discovery probes for the herd: the in-process device ports
// it was built with, plus whatever Register-mode devices have registered over
// the same socket. A registered device on this host is answered for directly;
// one on another host is relayed to, so its reply carries its own address (see
// goalpaca's DISCOVERY_RELAY.md). onRegister, when set, is called for every
// heartbeat, which is how the orchestrator page learns a separate binary's
// bound port.
type responder struct {
	mu         sync.Mutex // guards static and staticSet: addPort runs while probes are served
	static     [][]byte   // one {"AlpacaPort":N} per in-process port
	staticSet  map[int]bool
	reg        *alpacadev.Registrations
	onRegister func(*alpacadev.Registration)
}

// addPort adds an in-process port bound after start (a device enabled from
// the orchestrator page onto a new server).
func (r *responder) addPort(p int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.staticSet[p] {
		return
	}
	r.static = append(r.static, portReply(p))
	r.staticSet[p] = true
}

func newResponder(ports []int, onRegister func(*alpacadev.Registration)) *responder {
	r := &responder{staticSet: map[int]bool{}, reg: alpacadev.NewRegistrations(0), onRegister: onRegister}
	for _, p := range ports {
		r.static = append(r.static, portReply(p))
		r.staticSet[p] = true
	}
	return r
}

func portReply(p int) []byte {
	b, _ := json.Marshal(struct {
		AlpacaPort int `json:"AlpacaPort"`
	}{p})
	return b
}

// replies is the datagram list for one probe: the static ports plus the live
// local registrations, each port once.
func (r *responder) replies() [][]byte {
	r.mu.Lock()
	out := append([][]byte(nil), r.static...)
	set := r.staticSet
	r.mu.Unlock()
	for _, p := range r.reg.LocalPorts() {
		if !set[p] {
			out = append(out, portReply(p))
		}
	}
	return out
}

// handle processes one datagram: a probe draws the replies and a relay to
// every remote registration; a heartbeat is recorded. The relay runs off the
// read loop, since it waits on remote HTTP and heartbeats keep arriving.
func (r *responder) handle(ctx context.Context, c *net.UDPConn, src net.Addr, pkt []byte) {
	ua, _ := src.(*net.UDPAddr)
	if ua == nil {
		return
	}
	kind, entry := r.reg.Datagram(pkt, ua)
	switch kind {
	case alpacadev.DatagramProbe:
		for _, b := range r.replies() {
			_, _ = c.WriteTo(b, src) // one datagram per device port
		}
		go r.reg.Relay(ctx, ua, func(e alpacadev.Registration, err error) {
			log.Printf("alpacahurd: discovery relay to %s at %s:%d: %v", registrationLabel(e), e.Addr, e.AlpacaPort, err)
		})
	case alpacadev.DatagramHeartbeat:
		if r.onRegister != nil {
			r.onRegister(entry)
		}
	}
}

// registrationLabel names a registration for a log line: its instance, else
// its device name, else its UniqueID.
func registrationLabel(e alpacadev.Registration) string {
	switch {
	case e.Instance != "":
		return e.Instance
	case e.DeviceName != "":
		return e.DeviceName
	}
	return e.UniqueID
}

// runDiscovery answers Alpaca discovery probes on UDP 32227 through r. The socket
// is bound with SO_REUSEADDR/SO_REUSEPORT so it co-binds alongside other Alpaca
// servers on 32227. When ifaces is non-empty (i.e. "listen" scopes the herd),
// discovery answers only on those interfaces; a nil/empty ifaces answers on every
// interface.
func runDiscovery(ctx context.Context, r *responder, ipv6 bool, ifaces map[int]bool) error {
	lc := net.ListenConfig{Control: alpacadev.ReuseControl}
	pc, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("0.0.0.0:%d", discoveryPort))
	if err != nil {
		return err
	}
	go serveDiscovery(ctx, pc.(*net.UDPConn), r, ifaces)

	if ipv6 {
		if err := listenV6(ctx, lc, r, ifaces); err != nil {
			log.Printf("alpacahurd: discovery IPv6 disabled: %v", err)
		}
	}
	return nil
}

// listenV6 answers Alpaca IPv6 discovery probes. It binds one [::]:32227 socket and
// joins the Alpaca multicast group on every up, multicast-capable interface, so the
// herd is discoverable on all links. Best-effort: a per-interface join failure is
// skipped; only a total failure returns an error and leaves IPv4 discovery running.
func listenV6(ctx context.Context, lc net.ListenConfig, r *responder, ifaces map[int]bool) error {
	pc, err := lc.ListenPacket(ctx, "udp6", fmt.Sprintf("[::]:%d", discoveryPort))
	if err != nil {
		return err
	}
	conn := pc.(*net.UDPConn)
	group := &net.UDPAddr{IP: net.ParseIP(discoveryV6Group)}
	p := ipv6.NewPacketConn(conn)
	ifs, _ := net.Interfaces()
	var joined int
	for i := range ifs {
		ifi := ifs[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		if len(ifaces) > 0 && !ifaces[ifi.Index] {
			continue // "listen" scopes us off this interface
		}
		if err := p.JoinGroup(&ifi, group); err == nil {
			joined++
		}
	}
	if joined == 0 {
		conn.Close()
		return fmt.Errorf("no multicast-capable interface joined %s", discoveryV6Group)
	}
	log.Printf("alpacahurd: discovery IPv6 on [%s]:%d (%d interface(s))", discoveryV6Group, discoveryPort, joined)
	go serveDiscovery(ctx, conn, r, nil) // reception is already limited to joined interfaces
	return nil
}

// serveDiscovery reads datagrams on c and hands each to r. When ifaces is
// non-empty it acts only on datagrams that arrived on one of those interfaces
// (so a "listen"-scoped herd does not advertise on interfaces it isn't serving);
// a nil ifaces answers on all.
func serveDiscovery(ctx context.Context, c *net.UDPConn, r *responder, ifaces map[int]bool) {
	defer c.Close()

	// Interface-scoped path: read the inbound interface via a control message and
	// drop probes from interfaces we don't listen on. Falls back to answering all if
	// the OS won't report the inbound interface.
	var p *ipv4.PacketConn
	if len(ifaces) > 0 {
		p = ipv4.NewPacketConn(c)
		if err := p.SetControlMessage(ipv4.FlagInterface, true); err != nil {
			p = nil
		}
	}

	buf := make([]byte, 2048)
	for ctx.Err() == nil {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		var (
			n   int
			src net.Addr
			err error
		)
		if p != nil {
			var cm *ipv4.ControlMessage
			n, cm, src, err = p.ReadFrom(buf)
			if err == nil && cm != nil && !ifaces[cm.IfIndex] {
				continue // probe arrived on an interface we don't serve
			}
		} else {
			var ua *net.UDPAddr
			n, ua, err = c.ReadFromUDP(buf)
			src = ua
		}
		if err != nil {
			continue
		}
		r.handle(ctx, c, src, buf[:n])
	}
}
