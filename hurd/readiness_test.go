package hurd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadinessProbe(t *testing.T) {
	for _, tc := range []struct {
		connected string
		want      string
	}{
		{`{"Value":true}`, "Enabled · Working"},
		{`{"Value":false}`, "Enabled · Disconnected"},
		{`{"ErrorNumber":1,"ErrorMessage":"Hardware failure"}`, "Enabled · Error"},
		{`{}`, "Enabled · Not verified"},
	} {
		t.Run(tc.want+tc.connected, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("mutating request")
				}
				if r.URL.Path == "/management/v1/configureddevices" {
					w.Write([]byte(`{"Value":[{"DeviceType":"Camera","DeviceNumber":0}]}`))
					return
				}
				if r.URL.Path != "/api/v1/camera/0/connected" {
					t.Error(r.URL.Path)
				}
				w.Write([]byte(tc.connected))
			}))
			defer srv.Close()
			got := probeReadiness(context.Background(), srv.URL)
			expectedHardware := "Unknown"
			if tc.connected == `{"Value":true}` {
				expectedHardware = "Connected"
			}
			if tc.connected == `{"Value":false}` {
				expectedHardware = "Disconnected"
			}
			if got.Hardware != expectedHardware {
				t.Fatalf("hardware: %+v", got)
			}
			if got.State != tc.want {
				t.Fatalf("%+v", got)
			}
		})
	}
}
func TestReadinessConfiguration(t *testing.T) {
	for _, tc := range []struct{ config, want string }{
		{`{"driver":"sim-camera","enable":false}`, "Disabled · Configured"},
		{`{"driver":"sim-camera","enable":false,"pixelCountX":"bad"}`, "Disabled · Needs configuration"},
		{`{"driver":"sim-camera","enable":true,"pixelCountX":"bad"}`, "Enabled · Needs configuration"},
		{`{"driver":"sim-camera","enable":true}`, "Enabled · Not verified"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			o, path, _ := editorFixture(t)
			writeFile(t, path, tc.config)
			spec, err := loadDeviceFile(path, stateDevicesDir())
			if err != nil {
				t.Fatal(err)
			}
			o.rows[0].spec = spec
			w := logGet(o, "/setup/readiness?instance=cam")
			var result readinessResult
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.State != tc.want {
				t.Fatalf("%+v", result)
			}
		})
	}
}
