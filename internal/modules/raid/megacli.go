package raid

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"toolctl/internal/core"
)

var (
	megaAdapterHeader = regexp.MustCompile(`(?i)^Adapter\s+#?([0-9]+)(?:\s+--.*)?$`)
	megaVirtualDrive  = regexp.MustCompile(`(?i)^Virtual Drive:\s*([^\s(]+)`)
	megaPrimaryRAID   = regexp.MustCompile(`(?i)Primary-([0-9]+)`)
	megaTemperature   = regexp.MustCompile(`-?[0-9]+(?:\.[0-9]+)?`)
)

func runMegaCLI(ctx context.Context, deps core.Dependencies, binary string) (vendorInventory, error) {
	queries := []struct {
		name  string
		args  []string
		parse func([]byte, *vendorInventory) error
	}{
		{name: "adapter", args: []string{"-AdpAllInfo", "-aALL", "-NoLog"}, parse: parseMegaCLIAdapters},
		{name: "virtual drives", args: []string{"-LDInfo", "-Lall", "-aALL", "-NoLog"}, parse: parseMegaCLIVolumes},
		{name: "physical disks", args: []string{"-PDList", "-aALL", "-NoLog"}, parse: parseMegaCLIDisks},
	}
	inventory := newVendorInventory()
	succeeded := 0
	failures := []string{}
	for _, query := range queries {
		result := deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Args: query.args, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: 16 << 20, MaxStderr: 1 << 20})
		if result.TimedOut {
			failures = append(failures, query.name+" query timed out")
			continue
		}
		if result.Err != nil || result.ExitCode != 0 {
			failures = append(failures, query.name+" query failed: "+processMessage(result))
			continue
		}
		if err := query.parse(result.Stdout, &inventory); err != nil {
			failures = append(failures, query.name+" output could not be parsed: "+err.Error())
			continue
		}
		succeeded++
	}
	if succeeded == 0 {
		return vendorInventory{}, fmt.Errorf("megacli query failed: %s", strings.Join(failures, "; "))
	}
	if len(failures) > 0 {
		inventory.Incomplete["megacli"] = strings.Join(failures, "; ")
	}
	return inventory, nil
}

func parseMegaCLIAdapters(data []byte, inventory *vendorInventory) error {
	controller := -1
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if match := megaAdapterHeader.FindStringSubmatch(line); len(match) == 2 {
			controller = ensureMegaController(inventory, match[1])
			continue
		}
		if controller < 0 {
			continue
		}
		key, value, ok := splitVendorField(line)
		if !ok {
			continue
		}
		item := &inventory.Controllers[controller]
		switch strings.ToLower(key) {
		case "product name":
			item.Model = value
		case "serial no", "serial number":
			item.Serial = value
		case "fw package build", "firmware version", "fw version":
			item.Firmware = value
		case "controller status":
			item.VendorState, item.Health = value, normalizeHealth(value)
		}
	}
	if controller < 0 {
		return fmt.Errorf("no adapter found")
	}
	return nil
}

func parseMegaCLIVolumes(data []byte, inventory *vendorInventory) error {
	controller, volume := "", -1
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if match := megaAdapterHeader.FindStringSubmatch(line); len(match) == 2 {
			controller = match[1]
			ensureMegaController(inventory, controller)
			continue
		}
		if match := megaVirtualDrive.FindStringSubmatch(line); len(match) == 2 {
			if controller == "" {
				controller = "0"
				ensureMegaController(inventory, controller)
			}
			inventory.Volumes = append(inventory.Volumes, Volume{Controller: controller, ID: match[1], State: HealthUnknown, Backend: "megacli"})
			volume = len(inventory.Volumes) - 1
			continue
		}
		if volume < 0 {
			continue
		}
		key, value, ok := splitVendorField(line)
		if !ok {
			continue
		}
		item := &inventory.Volumes[volume]
		switch strings.ToLower(key) {
		case "name":
			item.Name = value
		case "raid level":
			item.RAIDLevel = normalizeMegaRAID(value)
		case "size":
			item.SizeBytes = parseVendorSize(value)
		case "state":
			item.VendorState, item.State = value, normalizeHealth(value)
		}
	}
	if volume < 0 {
		return fmt.Errorf("no virtual drive found")
	}
	return nil
}

func parseMegaCLIDisks(data []byte, inventory *vendorInventory) error {
	controller, disk := "", -1
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if match := megaAdapterHeader.FindStringSubmatch(line); len(match) == 2 {
			controller = match[1]
			ensureMegaController(inventory, controller)
			continue
		}
		key, value, ok := splitVendorField(line)
		if !ok {
			continue
		}
		if strings.EqualFold(key, "Enclosure Device ID") {
			if controller == "" {
				controller = "0"
				ensureMegaController(inventory, controller)
			}
			inventory.Disks = append(inventory.Disks, Disk{Controller: controller, Enclosure: value, State: HealthUnknown, Backend: "megacli"})
			disk = len(inventory.Disks) - 1
			continue
		}
		if disk < 0 {
			continue
		}
		item := &inventory.Disks[disk]
		switch strings.ToLower(key) {
		case "slot number":
			item.Slot = value
		case "device id":
			item.ID = value
		case "firmware state":
			item.VendorState, item.State = value, normalizeMegaDiskHealth(value)
		case "raw size", "coerced size":
			if item.SizeBytes == 0 {
				item.SizeBytes = parseVendorSize(value)
			}
		case "pd type":
			item.Interface = value
		case "media type":
			item.MediaType = value
		case "inquiry data":
			item.Model = value
		case "device firmware level":
			item.Firmware = value
		case "drive temperature":
			if number := megaTemperature.FindString(value); number != "" {
				if temperature, err := strconv.ParseFloat(number, 64); err == nil {
					item.TemperatureC = &temperature
				}
			}
		case "predictive failure count":
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			if count, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
				failed := count > 0
				item.PredictiveFailure = &failed
			}
		}
	}
	if disk < 0 {
		return fmt.Errorf("no physical disk found")
	}
	for index := range inventory.Disks {
		item := &inventory.Disks[index]
		if item.Backend == "megacli" && item.ID == "" {
			item.ID = firstNonEmpty(item.Slot, fmt.Sprintf("disk-%d", index))
		}
	}
	return nil
}

func ensureMegaController(inventory *vendorInventory, id string) int {
	for index := range inventory.Controllers {
		if inventory.Controllers[index].ID == id && inventory.Controllers[index].Backend == "megacli" {
			return index
		}
	}
	inventory.Controllers = append(inventory.Controllers, Controller{ID: id, Backend: "megacli", Vendor: "Broadcom/LSI", Health: HealthUnknown})
	return len(inventory.Controllers) - 1
}

func normalizeMegaRAID(value string) string {
	if match := megaPrimaryRAID.FindStringSubmatch(value); len(match) == 2 {
		return "RAID " + match[1]
	}
	return strings.TrimSpace(value)
}

func normalizeMegaDiskHealth(value string) Health {
	compact := strings.ToLower(strings.TrimSpace(strings.Split(value, ",")[0]))
	return normalizeHealth(compact)
}
