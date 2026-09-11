package hurd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// deleteDevice removes a disabled instance's configuration, retaining its binary
// and saved state. The device must be stopped before deleting its configuration.
func (o *orchestrator) deleteDevice(ctx context.Context, instance string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	var indexes []int
	for i, row := range o.rows {
		if instance != "" && row.spec.Instance == instance {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) == 0 {
		return fmt.Errorf("no configured device %q", instance)
	}
	row := o.rows[indexes[0]]
	if row.spec.Source == "" || filepath.Clean(filepath.Dir(row.spec.Source)) != filepath.Clean(devicesDirFor(o.cfgPath)) {
		return fmt.Errorf("only devices.d configurations can be deleted")
	}
	for _, i := range indexes {
		if o.rows[i].spec.enabled() || o.rows[i].inProcess {
			return fmt.Errorf("disable %s before deleting it", instance)
		}
	}
	if row.reg != nil && !row.reg.Local {
		return fmt.Errorf("delete this device on its own host")
	}
	if row.res.kind == installedBinary {
		if _, none := o.sup.(noSupervisor); !none {
			status, err := o.sup.Status(ctx, instance)
			if err != nil {
				return fmt.Errorf("could not verify that the device is stopped: %w", err)
			}
			if status.Running {
				return fmt.Errorf("stop %s before deleting it", instance)
			}
		}
	}
	// Recheck disk so an external enable cannot be silently discarded.
	spec, err := loadDeviceFile(row.spec.Source, stateDevicesDir())
	if err != nil {
		return err
	}
	if spec.enabled() {
		return fmt.Errorf("disable %s before deleting it", instance)
	}
	if err := os.Remove(row.spec.Source); err != nil {
		return err
	}
	o.replaceRows(indexes, nil)
	kept := o.cfg.Devices[:0]
	for _, spec := range o.cfg.Devices {
		if spec.Instance != instance {
			kept = append(kept, spec)
		}
	}
	o.cfg.Devices = kept
	return nil
}
