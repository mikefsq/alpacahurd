package hurd

import (
	"fmt"
	"net"
	"strconv"
)

// resolveListen expands interface names and IP literals into bind addresses
// and interface indexes. An empty list returns nil for wildcard binding.
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

// listenAddrsFor pairs hosts with port, using a wildcard when hosts is empty.
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
