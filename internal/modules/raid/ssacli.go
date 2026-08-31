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
	ssaControllerHeader = regexp.MustCompile(`(?i)^(.+?)\s+in\s+slot\s+([^\s(]+)`)
	ssaLogicalCompact   = regexp.MustCompile(`(?i)^logicaldrive\s+([^\s(]+)\s*\(([^,]+),\s*([^,]+),\s*([^)]+)\)`)
	ssaPhysicalHeader   = regexp.MustCompile(`(?i)^physicaldrive\s+([^\s(]+)`)
	ssaBayPattern       = regexp.MustCompile(`(?i)(?:bay|slot)\s*[:=]?\s*([0-9]+)`)
	ssaBoxPattern       = regexp.MustCompile(`(?i)(?:box|enclosure)\s*[:=]?\s*([0-9]+)`)
)

func runSSACLI(ctx context.Context, deps core.Dependencies, binary string) (vendorInventory, error) {
	result := deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Args: []string{"ctrl", "all", "show", "config", "detail"}, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: 16 << 20, MaxStderr: 1 << 20})
	if result.TimedOut {
		return vendorInventory{}, fmt.Errorf("ssacli query timed out")
	}
	if result.Err != nil || result.ExitCode != 0 {
		return vendorInventory{}, fmt.Errorf("ssacli query failed: %s", processMessage(result))
	}
	return parseSSACLI(result.Stdout)
}

func parseSSACLI(data []byte) (vendorInventory, error) {
	inventory := newVendorInventory()
	controllerIndex, volumeIndex, diskIndex := -1, -1, -1
	section := ""
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if match := ssaControllerHeader.FindStringSubmatch(line); len(match) == 3 {
			inventory.Controllers = append(inventory.Controllers, Controller{ID: match[2], Backend: "ssacli", Vendor: "HPE", Model: strings.TrimSpace(match[1]), Health: HealthUnknown})
			controllerIndex, volumeIndex, diskIndex, section = len(inventory.Controllers)-1, -1, -1, "controller"
			continue
		}
		if controllerIndex < 0 {
			continue
		}
		if match := ssaLogicalCompact.FindStringSubmatch(line); len(match) == 5 {
			inventory.Volumes = append(inventory.Volumes, Volume{Controller: inventory.Controllers[controllerIndex].ID, ID: match[1], RAIDLevel: strings.TrimSpace(match[3]), State: normalizeHealth(match[4]), VendorState: strings.TrimSpace(match[4]), SizeBytes: parseVendorSize(match[2]), Backend: "ssacli"})
			volumeIndex, diskIndex, section = len(inventory.Volumes)-1, -1, "volume"
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "logical drive:") {
			id := strings.TrimSpace(strings.TrimPrefix(line, line[:strings.Index(line, ":")+1]))
			inventory.Volumes = append(inventory.Volumes, Volume{Controller: inventory.Controllers[controllerIndex].ID, ID: id, State: HealthUnknown, Backend: "ssacli"})
			volumeIndex, diskIndex, section = len(inventory.Volumes)-1, -1, "volume"
			continue
		}
		if match := ssaPhysicalHeader.FindStringSubmatch(line); len(match) == 2 {
			disk := Disk{Controller: inventory.Controllers[controllerIndex].ID, ID: match[1], State: HealthUnknown, Backend: "ssacli"}
			parts := strings.Split(match[1], ":")
			if len(parts) >= 2 {
				disk.Enclosure, disk.Slot = parts[len(parts)-2], parts[len(parts)-1]
			}
			if match := ssaBoxPattern.FindStringSubmatch(line); len(match) == 2 {
				disk.Enclosure = match[1]
			}
			if match := ssaBayPattern.FindStringSubmatch(line); len(match) == 2 {
				disk.Slot = match[1]
			}
			inventory.Disks = append(inventory.Disks, disk)
			diskIndex, volumeIndex, section = len(inventory.Disks)-1, -1, "disk"
			continue
		}
		key, value, ok := splitVendorField(line)
		if !ok {
			continue
		}
		switch section {
		case "controller":
			controller := &inventory.Controllers[controllerIndex]
			switch strings.ToLower(key) {
			case "controller status", "status":
				controller.VendorState, controller.Health = value, normalizeHealth(value)
			case "serial number":
				controller.Serial = value
			case "firmware version":
				controller.Firmware = value
			}
		case "volume":
			volume := &inventory.Volumes[volumeIndex]
			switch strings.ToLower(key) {
			case "status":
				volume.VendorState, volume.State = value, normalizeHealth(value)
			case "fault tolerance", "raid level":
				volume.RAIDLevel = normalizeSSARaid(value)
			case "size":
				volume.SizeBytes = parseVendorSize(value)
			case "logical drive label", "name":
				volume.Name = value
			case "disk name", "mount points":
				if fields := strings.Fields(value); len(fields) > 0 {
					volume.DevicePath = fields[0]
				}
			}
		case "disk":
			disk := &inventory.Disks[diskIndex]
			switch strings.ToLower(key) {
			case "status":
				disk.VendorState, disk.State = value, normalizeHealth(value)
			case "box", "enclosure":
				disk.Enclosure = value
			case "bay", "slot":
				disk.Slot = value
			case "interface type":
				disk.Interface = value
			case "drive type":
				disk.MediaType = value
			case "size":
				disk.SizeBytes = parseVendorSize(value)
			case "model":
				disk.Model = value
			case "serial number":
				disk.Serial = value
			case "firmware revision":
				disk.Firmware = value
			case "current temperature (c)":
				if temperature, err := strconv.ParseFloat(strings.Fields(value)[0], 64); err == nil {
					disk.TemperatureC = &temperature
				}
			}
		}
	}
	if len(inventory.Controllers) == 0 {
		return vendorInventory{}, fmt.Errorf("ssacli output does not contain a controller")
	}
	return inventory, nil
}

func splitVendorField(line string) (string, string, bool) {
	key, value, ok := strings.Cut(line, ":")
	return strings.TrimSpace(key), strings.TrimSpace(value), ok
}

func normalizeSSARaid(value string) string {
	value = strings.TrimSpace(value)
	if value == "1+0" {
		return "RAID 10"
	}
	if strings.HasPrefix(strings.ToUpper(value), "RAID") {
		return value
	}
	return "RAID " + value
}

func processMessage(result core.ProcessResult) string {
	message := strings.TrimSpace(string(result.Stderr))
	if message == "" {
		message = strings.TrimSpace(string(result.Stdout))
	}
	if message == "" && result.Err != nil {
		message = result.Err.Error()
	}
	if message == "" {
		message = fmt.Sprintf("exit code %d", result.ExitCode)
	}
	if len(message) > 4096 {
		message = message[:4093] + "..."
	}
	return message
}
