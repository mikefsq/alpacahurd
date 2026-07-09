package hurd

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/ipv6"
)

// TestDiscoveryIPv6RoundTrip sends a real Alpaca IPv6 multicast probe and asserts
// the hurd responder replies with the advertised AlpacaPort. The probe is sent the
// way a real Alpaca client sends it — with the egress multicast interface set
// explicitly — on each multicast-capable interface, so this exercises the
// responder's multi-interface JoinGroup path end to end. Skips only if the host has
// no IPv6 multicast interface at all.
func TestDiscoveryIPv6RoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const advertised = 41999
	if err := runDiscovery(ctx, []int{advertised}, true, nil); err != nil {
		t.Fatalf("runDiscovery: %v", err)
	}

	ifs := multicastInterfaces()
	if len(ifs) == 0 {
		t.Skip("no IPv6 multicast-capable interface")
	}
	group := net.ParseIP(discoveryV6Group)
	want := []byte(`"AlpacaPort":41999`)
	for i := range ifs {
		ifi := ifs[i]
		if probeReply(t, &ifi, group, want) {
			t.Logf("IPv6 discovery reply received over %s", ifi.Name)
			return // a reply on any interface proves multicast discoverability
		}
	}
	t.Fatalf("no IPv6 discovery reply on any of %d interface(s)", len(ifs))
}

// probeReply sends one Alpaca discovery probe to the multicast group out ifi and
// reports whether a matching reply came back within a second. It sets the egress
// multicast interface explicitly (as a real client does); a bare zoned DialUDP does
// not reliably set IPV6_MULTICAST_IF on macOS.
func probeReply(t *testing.T, ifi *net.Interface, group, want []byte) bool {
	t.Helper()
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{})
	if err != nil {
		return false
	}
	defer conn.Close()
	p := ipv6.NewPacketConn(conn)
	if err := p.SetMulticastInterface(ifi); err != nil {
		return false
	}
	dst := &net.UDPAddr{IP: append(net.IP(nil), group...), Port: discoveryPort, Zone: ifi.Name}
	if _, err := conn.WriteToUDP([]byte(discoveryToken+"1"), dst); err != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 512)
	n, _, err := conn.ReadFromUDP(buf)
	return err == nil && bytes.Contains(buf[:n], want)
}

// multicastInterfaces returns up, multicast-capable, non-loopback interfaces that
// have an IPv6 address — the ones a client would probe over.
func multicastInterfaces() []net.Interface {
	var out []net.Interface
	ifs, _ := net.Interfaces()
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() == nil && ipn.IP.To16() != nil {
				out = append(out, ifi)
				break
			}
		}
	}
	return out
}
