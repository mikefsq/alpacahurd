package hurd

import (
	"net"
	"strings"
	"testing"
)

func TestResolveListenInterfaceBindsBothStacks(t *testing.T) {
	lo := loopbackName(t)
	addrs, ifaces, err := resolveListen([]string{lo})
	if err != nil {
		t.Fatalf("resolveListen(%q): %v", lo, err)
	}
	if len(ifaces) == 0 {
		t.Fatalf("resolveListen(%q) recorded no interface for discovery scoping", lo)
	}
	var v4, v6 bool
	for _, h := range addrs {
		ip := net.ParseIP(strings.Split(h, "%")[0])
		if ip == nil {
			t.Fatalf("resolved host %q is not an IP", h)
		}
		if ip.To4() != nil {
			v4 = true
		} else {
			v6 = true
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(h, "0"))
		if err != nil {
			t.Fatalf("listen %s: %v", h, err)
		}
		ln.Close()
	}
	if !v4 || !v6 {
		t.Fatalf("loopback %q resolved to %v; want both stacks (v4=%v v6=%v)", lo, addrs, v4, v6)
	}
}

func TestResolveListenIPLiteralIsSingleStack(t *testing.T) {
	addrs, _, err := resolveListen([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 1 || addrs[0] != "127.0.0.1" {
		t.Fatalf("resolveListen([127.0.0.1]) = %v; want [127.0.0.1]", addrs)
	}
}

// loopbackName returns a dual-stack loopback interface or skips the test.
func loopbackName(t *testing.T) string {
	t.Helper()
	ifs, _ := net.Interfaces()
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagLoopback == 0 || ifi.Flags&net.FlagUp == 0 {
			continue
		}
		var v4, v6 bool
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ipn.IP.To4() != nil {
					v4 = true
				} else {
					v6 = true
				}
			}
		}
		if v4 && v6 {
			return ifi.Name
		}
	}
	t.Skip("no dual-stack loopback interface")
	return ""
}
