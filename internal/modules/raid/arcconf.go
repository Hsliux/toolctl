package raid

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"toolctl/internal/core"
)

var (
	arcControllerID = regexp.MustCompile(`(?i)controller\s+([0-9]+)`)
	arcLogicalID    = regexp.MustCompile(`(?i)^logical\s+device\s+number\s+([0-9]+)`)
	arcDeviceID     = regexp.MustCompile(`(?i)^device\s+#?([0-9]+)`)
	arcLocation     = regexp.MustCompile(`(?i)enclosure\s+([^,]+),\s*slot\s+([^,\s]+)`)
)

func runArcconf(ctx context.Context, deps core.Dependencies, binary string) (vendorInventory, error) {
	listed := deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Args: []string{"LIST", "nologs"}, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: 4 << 20, MaxStderr: 1 << 20})
	if listed.TimedOut {
		return vendorInventory{}, fmt.Errorf("arcconf controller list timed out")
	}
	if listed.Err != nil || listed.ExitCode != 0 {
		return vendorInventory{}, fmt.Errorf("arcconf controller list failed: %s", processMessage(listed))
	}
	ids := arcconfControllerIDs(string(listed.Stdout))
	if len(ids) == 0 {
		return vendorInventory{}, fmt.Errorf("arcconf did not report any controller IDs")
	}
	inventory := newVendorInventory()
	for _, id := range ids {
		result := deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Args: []string{"GETCONFIG", id, "nologs"}, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: 16 << 20, MaxStderr: 1 << 20})
		if result.TimedOut || result.Err != nil || result.ExitCode != 0 {
			inventory.Incomplete["arcconf"] = "controller " + id + " query failed: " + processMessage(result)
			continue
		}
		part, err := parseArcconf(result.Stdout, id)
		if err != nil {
			inventory.Incomplete["arcconf"] = "controller " + id + " output could not be parsed: " + err.Error()
			continue
		}
		inventory.Controllers = append(inventory.Controllers, part.Controllers...)
		inventory.Volumes = append(inventory.Volumes, part.Volumes...)
		inventory.Disks = append(inventory.Disks, part.Disks...)
	}
	if len(inventory.Controllers) == 0 {
		return vendorInventory{}, fmt.Errorf("arcconf inventory could not be collected")
	}
	return inventory, nil
}

func arcconfControllerIDs(output string) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, line := range strings.Split(output, "\n") {
		match := arcControllerID.FindStringSubmatch(line)
		if len(match) == 2 && !seen[match[1]] {
			seen[match[1]] = true
			ids = append(ids, match[1])
		}
	}
	sort.Strings(ids)
	return ids
}

func parseArcconf(data []byte, controllerID string) (vendorInventory, error) {
	inventory := newVendorInventory()
	inventory.Controllers = append(inventory.Controllers, Controller{ID: controllerID, Backend: "arcconf", Vendor: "Microchip/Adaptec", Health: HealthUnknown})
	volumeIndex, diskIndex, section := -1, -1, "controller"
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "---") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "logical device information") {
			section = "volumes"
			continue
		}
		if strings.HasPrefix(lower, "physical device information") {
			section = "disks"
			continue
		}
		if match := arcLogicalID.FindStringSubmatch(line); len(match) == 2 {
			inventory.Volumes = append(inventory.Volumes, Volume{Controller: controllerID, ID: match[1], State: HealthUnknown, Backend: "arcconf"})
			volumeIndex, diskIndex, section = len(inventory.Volumes)-1, -1, "volume"
			continue
		}
		if match := arcDeviceID.FindStringSubmatch(line); len(match) == 2 && (section == "disks" || section == "disk") {
			inventory.Disks = append(inventory.Disks, Disk{Controller: controllerID, ID: match[1], State: HealthUnknown, Backend: "arcconf"})
			diskIndex, volumeIndex, section = len(inventory.Disks)-1, -1, "disk"
			continue
		}
		key, value, ok := splitVendorField(line)
		if !ok {
			continue
		}
		key = strings.ToLower(key)
		switch section {
		case "controller", "volumes", "disks":
			controller := &inventory.Controllers[0]
			switch key {
			case "controller status":
				controller.VendorState, controller.Health = value, normalizeHealth(value)
			case "controller model":
				controller.Model = value
			case "controller serial number", "serial number":
				controller.Serial = value
			case "firmware":
				controller.Firmware = value
			}
		case "volume":
			volume := &inventory.Volumes[volumeIndex]
			switch key {
			case "logical device name":
				volume.Name = value
			case "raid level":
				volume.RAIDLevel = "RAID " + strings.TrimPrefix(strings.ToUpper(value), "RAID ")
			case "status of logical device", "state":
				volume.VendorState, volume.State = value, normalizeHealth(value)
			case "size":
				volume.SizeBytes = parseVendorSize(value)
			case "mount points":
				if fields := strings.Fields(value); len(fields) > 0 {
					volume.DevicePath = fields[0]
				}
			}
		case "disk":
			disk := &inventory.Disks[diskIndex]
			switch key {
			case "state":
				disk.VendorState, disk.State = value, normalizeHealth(value)
			case "reported channel,device(t:l)":
				location := strings.Split(value, "(")[0]
				parts := strings.Split(location, ",")
				if len(parts) == 2 {
					// Channel/device identifies a disk, not its physical enclosure/bay.
					disk.ID = strings.TrimSpace(parts[0]) + ":" + strings.TrimSpace(parts[1])
				}
			case "reported location":
				if match := arcLocation.FindStringSubmatch(value); len(match) == 3 {
					disk.Enclosure, disk.Slot = strings.TrimSpace(match[1]), strings.TrimSpace(match[2])
				}
			case "model":
				disk.Model = value
			case "serial number":
				disk.Serial = value
			case "firmware":
				disk.Firmware = value
			case "total size":
				disk.SizeBytes = parseVendorSize(value)
			case "transfer speed":
				disk.Interface = value
			case "device type":
				disk.MediaType = value
			}
		}
	}
	return inventory, nil
}
