package hurd

import (
	"fmt"
	"sort"
	"strings"
)

// portClashes describes enabled devices that cannot share their port. Devices served in this
// process share one server per port, but each installed driver binary is its own process with
// its own listener, so a second process on a port fails to bind and restarts in a loop.
func portClashes(rows []orchRow) []string {
	type users struct{ separate, inProcess []string }
	byPort := map[int]*users{}
	seen := map[string]bool{}
	for _, row := range rows {
		if !row.spec.enabled() || row.port == 0 {
			continue
		}
		name := row.spec.Instance
		if name == "" {
			name = row.spec.Driver
		}
		key := fmt.Sprintf("%d/%s", row.port, name)
		if seen[key] {
			continue // the blocks of one multi-device entry
		}
		seen[key] = true
		u := byPort[row.port]
		if u == nil {
			u = &users{}
			byPort[row.port] = u
		}
		if row.inProcess || row.res.kind != installedBinary {
			u.inProcess = append(u.inProcess, name)
		} else {
			u.separate = append(u.separate, name)
		}
	}
	ports := make([]int, 0, len(byPort))
	for p := range byPort {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	var out []string
	for _, p := range ports {
		u := byPort[p]
		if len(u.separate) == 0 || len(u.separate)+len(u.inProcess) < 2 {
			continue
		}
		all := append(append([]string{}, u.separate...), u.inProcess...)
		sort.Strings(all)
		out = append(out, fmt.Sprintf("Port clash on %d: %s. %s run%s as a separate driver program, and only one program can listen on a port. Give each a different port.",
			p, strings.Join(all, " and "), strings.Join(u.separate, " and "), map[bool]string{true: "s", false: ""}[len(u.separate) == 1]))
	}
	return out
}
