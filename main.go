// Command alpacahurd runs a herd of ASCOM Alpaca device drivers in one process
// on a Raspberry Pi (or any Linux/macOS box): one binary, one config file, one
// discovery responder, hotplug-tolerant. Which drivers are compiled in is
// selected by hurd.conf (see drivers_gen.go); which devices run is declared in
// a JSON config (see config/hurd.example.json).
package main

import "github.com/mikefsq/alpacahurd/hurd"

func main() { hurd.Main() }
