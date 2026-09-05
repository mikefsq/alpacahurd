package hurd

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/ipv6"
)

func TestDiscoveryIPv6RoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const advertised = 41999
	if err := runDiscovery(ctx, newResponder([]int{advertised}, nil), true, nil); err != nil {
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

// probeReply sends a multicast probe through ifi and waits for a reply.
// The explicit egress interface is required on macOS.
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

// multicastInterfaces returns active non-loopback interfaces with IPv6 multicast support.
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
