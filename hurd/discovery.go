package hurd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
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

// runDiscovery answers Alpaca discovery probes on UDP 32227 for every fleet device
// port, sending one {"AlpacaPort":N} datagram per port. The socket is bound with
// SO_REUSEADDR/SO_REUSEPORT so it co-binds alongside other Alpaca servers on 32227.
// When ifaces is non-empty (i.e. "listen" scopes the fleet), discovery answers only
// on those interfaces; a nil/empty ifaces answers on every interface.
func runDiscovery(ctx context.Context, ports []int, ipv6 bool, ifaces map[int]bool) error {
	resp := make([][]byte, len(ports))
	for i, p := range ports {
		resp[i], _ = json.Marshal(struct {
			AlpacaPort int `json:"AlpacaPort"`
		}{p})
	}

	lc := net.ListenConfig{Control: alpacadev.ReuseControl}
	pc, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("0.0.0.0:%d", discoveryPort))
	if err != nil {
		return err
	}
	go serveDiscovery(ctx, pc.(*net.UDPConn), resp, ifaces)

	if ipv6 {
		if err := listenV6(ctx, lc, resp, ifaces); err != nil {
			log.Printf("alpacahurd: discovery IPv6 disabled: %v", err)
		}
	}
	return nil
}

// listenV6 answers Alpaca IPv6 discovery probes. It binds one [::]:32227 socket and
// joins the Alpaca multicast group on every up, multicast-capable interface, so the
// fleet is discoverable on all links. Best-effort: a per-interface join failure is
// skipped; only a total failure returns an error and leaves IPv4 discovery running.
func listenV6(ctx context.Context, lc net.ListenConfig, resp [][]byte, ifaces map[int]bool) error {
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
	go serveDiscovery(ctx, conn, resp, nil) // reception is already limited to joined interfaces
	return nil
}

// serveDiscovery answers probes on c. When ifaces is non-empty it replies only to
// probes that arrived on one of those interfaces (so a "listen"-scoped fleet does
// not advertise on interfaces it isn't serving); a nil ifaces answers on all.
func serveDiscovery(ctx context.Context, c *net.UDPConn, resp [][]byte, ifaces map[int]bool) {
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
		if !bytes.HasPrefix(bytes.TrimSpace(buf[:n]), []byte(discoveryToken)) {
			continue
		}
		for _, b := range resp {
			_, _ = c.WriteTo(b, src) // one datagram per device port
		}
	}
}
