package hurd

import (
	alpacadev "github.com/mikefsq/goalpaca/server"
	"testing"
	"time"
)

func TestDevicePageIdentity(t *testing.T) {
	spec := parseSpec(t, `{"driver":"sim-camera","name":"Guide","device":3,"enable":false}`)
	spec.Instance = "guide"
	row := orchRow{spec: spec}
	pr := devicePageRow(row)
	if pr.Type != "camera" || pr.Name != "Guide" || pr.Num != "3" {
		t.Fatalf("disabled: %+v", pr)
	}
	row.reg = &alpacadev.Registration{Heartbeat: alpacadev.Heartbeat{DeviceType: "telescope", DeviceName: "Reported mount"}, Seen: time.Now()}
	pr = devicePageRow(row)
	if pr.Type != "telescope" || pr.Name != "Reported mount" {
		t.Fatalf("heartbeat: %+v", pr)
	}
	row.reg.Seen = time.Now().Add(-time.Hour)
	pr = devicePageRow(row)
	if pr.Type != "camera" || pr.Name != "Guide" {
		t.Fatalf("expired: %+v", pr)
	}
	row.inProcess, row.num, row.deviceName, row.devType = true, 5, "Live camera", alpacadev.DeviceType("camera")
	pr = devicePageRow(row)
	if pr.Num != "5" || pr.Name != "Live camera" {
		t.Fatalf("running: %+v", pr)
	}
	row = orchRow{spec: parseSpec(t, `{"driver":"external-driver"}`)}
	row.spec.Instance = "mount"
	pr = devicePageRow(row)
	if pr.Type != "Unknown" || pr.Name != "mount" || pr.Num != "Automatic" {
		t.Fatalf("unknown: %+v", pr)
	}
}
