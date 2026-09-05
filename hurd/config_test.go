package hurd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func TestResolveConfigPath(t *testing.T) {
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
	chdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "hurd.json"), []byte(`{"devices":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveConfigPath(""); err != nil || got != "hurd.json" {
		t.Fatalf("cwd search: got %q, %v; want hurd.json", got, err)
	}
}

func TestResolveConfigPathNotFound(t *testing.T) {
	// A deployed box may hold the service's config, which would legitimately be
	// found; skip the not-found assertion there.
	sys := filepath.Join(alpacadev.SystemConfigDir(serverName), "hurd.json")
	if _, err := os.Stat(sys); err == nil {
		t.Skipf("%s exists on this host", sys)
	}
	t.Setenv("ALPACAHURD_CONFIG", "")
	t.Setenv("ALPACA_CONFIG_DIR", "")
	chdir(t, t.TempDir()) // empty dir: no ./hurd.json
	_, err := resolveConfigPath("")
	if err == nil {
		t.Fatal("want an error when no config file exists anywhere")
	}
	// The searched list names the platform's system-wide location.
	if !strings.Contains(err.Error(), sys) {
		t.Errorf("error should list %s, got: %v", sys, err)
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

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"prt":1,"devices":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(bad); err == nil {
		t.Error("unknown top-level config field should be rejected")
	}
}

func TestLoadConfigJSONC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hurd.json")
	const body = `// Server settings
{
  "discovery": "off", /* No discovery */
  "devices": [
    {
      // Inline device settings
      "driver": "sim-focuser",
      "name": "http://host/*literal*/"
    }
  ]
} // End of configuration
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Discovery != "off" || len(cfg.Devices) != 1 {
		t.Fatalf("parsed config wrong: %+v", cfg)
	}
	if got := cfg.Devices[0].Name; got != "http://host/*literal*/" {
		t.Fatalf("name = %q; comment markers in strings must be preserved", got)
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{"unknown field", "{/* comment */\"discovry\":\"off\"}"},
		{"trailing comma", "{\"discovery\":\"off\", /* comment */}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

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
	// Front-end keys are reserved by the registry but read by drivers.
	shared := map[string]bool{}
	for _, k := range registry.CommonKeys() {
		shared[k] = true
	}
	for _, k := range engine {
		if !shared[k] {
			t.Fatalf("deviceCommon key %q is not in registry.CommonKeys %v", k, registry.CommonKeys())
		}
	}
}

// chdir changes the working directory until test cleanup.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
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
