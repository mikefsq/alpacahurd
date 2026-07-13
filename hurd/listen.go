package hurd

import (
	"fmt"
	"net"
	"strconv"
)

// resolveListen expands the config "listen" entries into the concrete host addresses
// every TCP front-end binds to, plus the set of interface indexes those addresses
// live on (used to scope discovery to where the herd listens).
//
// Each entry is either an interface name or an IP literal:
//   - An interface name (e.g. "en0", "eth0", "lo") expands to all of that interface's
//     addresses — IPv4, IPv6 global, and IPv6 link-local (kept with its %zone).
//   - An IP literal binds exactly that one address. A bare IPv4 literal serves IPv4
//     only; name the interface instead to also serve its IPv6.
//
// An empty list returns (nil, nil): callers bind the wildcard ":port" (every
// interface, both stacks).
func resolveListen(entries []string) ([]string, map[int]bool, error) {
	if len(entries) == 0 {
		return nil, nil, nil
	}
	var addrs []string
	ifaces := map[int]bool{}
	for _, e := range entries {
		if ip := net.ParseIP(e); ip != nil {
			addrs = append(addrs, e)
			if idx := ifaceIndexForIP(ip); idx != 0 {
				ifaces[idx] = true
			}
			continue
		}
		ifi, err := net.InterfaceByName(e)
		if err != nil {
			return nil, nil, fmt.Errorf("listen %q: not an IP address or interface name: %w", e, err)
		}
		as, err := ifi.Addrs()
		if err != nil {
			return nil, nil, fmt.Errorf("listen %q: %w", e, err)
		}
		n := 0
		for _, a := range as {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			host := ipn.IP.String()
			if ipn.IP.To4() == nil && ipn.IP.IsLinkLocalUnicast() {
				host += "%" + ifi.Name // zone is required to bind a link-local address
			}
			addrs = append(addrs, host)
			n++
		}
		if n == 0 {
			return nil, nil, fmt.Errorf("listen %q: interface has no addresses", e)
		}
		ifaces[ifi.Index] = true
	}
	return addrs, ifaces, nil
}

// listenAddrsFor returns the "host:port" addresses a front-end on port should bind,
// given the resolved listen hosts. An empty hosts list yields the single wildcard
// ":port" (every interface).
func listenAddrsFor(port int, hosts []string) []string {
	if len(hosts) == 0 {
		return []string{fmt.Sprintf(":%d", port)}
	}
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = net.JoinHostPort(h, strconv.Itoa(port))
	}
	return out
}

// listenLines returns human-readable listen labels for logging (the bind addresses,
// or ":port (all interfaces)" for the wildcard default).
func listenLines(port int, hosts []string) []string {
	if len(hosts) == 0 {
		return []string{fmt.Sprintf(":%d (all interfaces)", port)}
	}
	return listenAddrsFor(port, hosts)
}

// ifaceIndexForIP returns the index of the interface that owns ip, or 0 if none.
func ifaceIndexForIP(ip net.IP) int {
	ifs, _ := net.Interfaces()
	for _, ifi := range ifs {
		as, _ := ifi.Addrs()
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(ip) {
				return ifi.Index
			}
		}
	}
	return 0
}
