package hurd

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// A flash carries an action's result across the redirect that follows the action, so the page
// the browser keeps in its history is a GET: reloading it repeats nothing. The message stays on
// the server, keyed by a random id, so a crafted link cannot put text into the banner.
type flash struct {
	text, kind string
	expires    time.Time
}

// maxFlashes bounds the table; a full table drops the oldest message.
const maxFlashes = 64

var flashes = struct {
	sync.Mutex
	m map[string]flash
}{m: map[string]flash{}}

// redirectWithBanner answers a POST with a 303 to the device list, which shows text once.
func redirectWithBanner(w http.ResponseWriter, r *http.Request, text, kind string) {
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	now := time.Now()
	flashes.Lock()
	for k, f := range flashes.m {
		if now.After(f.expires) {
			delete(flashes.m, k)
		}
	}
	for len(flashes.m) >= maxFlashes {
		oldest := ""
		for k, f := range flashes.m {
			if oldest == "" || f.expires.Before(flashes.m[oldest].expires) {
				oldest = k
			}
		}
		delete(flashes.m, oldest)
	}
	flashes.m[id] = flash{text, kind, now.Add(time.Minute)}
	flashes.Unlock()
	http.Redirect(w, r, "/setup?"+url.Values{"flash": {id}}.Encode(), http.StatusSeeOther)
}

// takeFlash returns and removes the message stored under id.
func takeFlash(id string) (text, kind string, ok bool) {
	flashes.Lock()
	defer flashes.Unlock()
	f, ok := flashes.m[id]
	delete(flashes.m, id)
	if !ok || time.Now().After(f.expires) {
		return "", "", false
	}
	return f.text, f.kind, true
}
