package hurd

import (
	"os"
	"strings"
)

// hostHome is this host's landing page, http://<hostname>.local/ on port 80. alpacahurd runs on a
// non-privileged port; the landing page on port 80 links it and the host's other services, and
// every alpacahurd page links back to it by the host's mDNS name.
func hostHome() (url, label string) {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "", ""
	}
	h, _, _ = strings.Cut(strings.ToLower(h), ".")
	return "http://" + h + ".local/", h + ".local"
}
