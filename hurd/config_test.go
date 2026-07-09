package hurd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mikefsq/goalpaca/registry"
)

func TestResolveConfigPath(t *testing.T) {
	// An explicit -config value always wins and is returned verbatim (not stat'd) —
	// so an explicit path that doesn't exist still surfaces as a LoadConfig read error.
	if got, err := resolveConfigPath("/some/explicit.json"); err != nil || got != "/some/explicit.json" {
		t.Fatalf("explicit: got %q, %v; want /some/explicit.json", got, err)
	}

	// $ALPACAHURD_CONFIG overrides the search when no explicit flag is given.
	t.Setenv("ALPACAHURD_CONFIG", "/from/env.json")
	if got, err := resolveConfigPath(""); err != nil || got != "/from/env.json" {
		t.Fatalf("env override: got %q, %v; want /from/env.json", got, err)
	}

	// With no flag and no env, the current directory's hurd.json is found first.
	t.Setenv("ALPACAHURD_CONFIG", "")
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "hurd.json"), []byte(`{"devices":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveConfigPath(""); err != nil || got != "hurd.json" {
		t.Fatalf("cwd search: got %q, %v; want hurd.json", got, err)
	}
}

func TestResolveConfigPathNotFound(t *testing.T) {
	// A deployed box may actually have /etc/alpacahurd/hurd.json, which would
	// legitimately be found — skip the not-found assertion there.
	if _, err := os.Stat("/etc/alpacahurd/hurd.json"); err == nil {
		t.Skip("/etc/alpacahurd/hurd.json exists on this host")
	}
	t.Setenv("ALPACAHURD_CONFIG", "")
	t.Chdir(t.TempDir()) // empty dir → no ./hurd.json
	_, err := resolveConfigPath("")
	if err == nil {
		t.Fatal("want an error when no config file exists anywhere")
	}
	if !strings.Contains(err.Error(), "/etc/alpacahurd/hurd.json") {
		t.Errorf("error should list the searched paths, got: %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hurd.json")
	const body = `{"discovery":"off","listen":["lo0"],"devices":[{"driver":"oasisfoc"},{"driver":"tenmicron","addr":"x:1"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Discovery != "off" || len(cfg.Listen) != 1 || len(cfg.Devices) != 2 {
		t.Fatalf("parsed config wrong: %+v", cfg)
	}
	// The raw entry is kept for the driver's own strict decode.
	if !strings.Contains(string(cfg.Devices[1].Raw), `"addr":"x:1"`) {
		t.Fatalf("device Raw not preserved: %s", cfg.Devices[1].Raw)
	}

	// Unknown TOP-LEVEL field is rejected (catches config typos). Unknown keys
	// inside a device entry are the driver's strict Decode's job instead.
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"prt":1,"devices":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bad); err == nil {
		t.Error("unknown top-level config field should be rejected")
	}
}

// TestCommonKeysMatchRegistry keeps the engine's deviceCommon in lock-step with
// registry.CommonKeys: the registry strips exactly these keys before a driver's
// strict decode, so a field added on one side but not the other either leaks an
// engine key into driver decodes (spurious "unknown field") or silently drops a
// driver key from them.
func TestCommonKeysMatchRegistry(t *testing.T) {
	var engine []string
	rt := reflect.TypeFor[deviceCommon]()
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("deviceCommon field %s has no json key", rt.Field(i).Name)
		}
		engine = append(engine, name)
	}
	shared := registry.CommonKeys()
	sort.Strings(engine)
	sort.Strings(shared)
	if !reflect.DeepEqual(engine, shared) {
		t.Fatalf("deviceCommon keys %v != registry.CommonKeys %v", engine, shared)
	}
}

// parseSpec builds a DeviceSpec from a JSON entry the way LoadConfig does.
func parseSpec(t *testing.T, entry string) DeviceSpec {
	t.Helper()
	var spec DeviceSpec
	if err := json.Unmarshal([]byte(entry), &spec); err != nil {
		t.Fatalf("parseSpec(%s): %v", entry, err)
	}
	return spec
}
