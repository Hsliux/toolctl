package raid

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"toolctl/internal/core"
)

type vendorInventory struct {
	Controllers []Controller
	Volumes     []Volume
	Disks       []Disk
	Incomplete  map[string]string
}

func runStorCLI(ctx context.Context, deps core.Dependencies, backend, binary string) (vendorInventory, error) {
	queries := [][]string{{"/call", "show", "J"}, {"/call/vall", "show", "J"}, {"/call/eall/sall", "show", "J"}}
	inventory := newVendorInventory()
	for index, args := range queries {
		result := deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Args: args, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: 16 << 20, MaxStderr: 1 << 20})
		if result.TimedOut {
			if index == 0 {
				return vendorInventory{}, fmt.Errorf("%s query timed out", backend)
			}
			inventory.Incomplete[backend] = fmt.Sprintf("query %q timed out", strings.Join(args, " "))
			continue
		}
		if result.Err != nil || result.ExitCode != 0 {
			if index > 0 {
				inventory.Incomplete[backend] = fmt.Sprintf("query %q failed", strings.Join(args, " "))
				continue
			}
			message := strings.TrimSpace(string(result.Stderr))
			if message == "" && result.Err != nil {
				message = result.Err.Error()
			}
			return vendorInventory{}, fmt.Errorf("%s query failed: %s", backend, message)
		}
		part, err := parseStorCLI(result.Stdout, backend)
		if err != nil {
			if index == 0 {
				return vendorInventory{}, err
			}
			inventory.Incomplete[backend] = fmt.Sprintf("query %q returned unparseable data", strings.Join(args, " "))
			continue
		}
		inventory.Controllers = mergeControllers(inventory.Controllers, part.Controllers)
		inventory.Volumes = append(inventory.Volumes, part.Volumes...)
		inventory.Disks = append(inventory.Disks, part.Disks...)
		for name, reason := range part.Incomplete {
			inventory.Incomplete[name] = reason
		}
	}
	return inventory, nil
}

func mergeControllers(current, incoming []Controller) []Controller {
	byID := map[string]int{}
	for index, item := range current {
		byID[item.ID] = index
	}
	for _, item := range incoming {
		if index, ok := byID[item.ID]; ok {
			if item.Model != "" {
				current[index].Model = item.Model
			}
			if item.Serial != "" {
				current[index].Serial = item.Serial
			}
			if item.Firmware != "" {
				current[index].Firmware = item.Firmware
			}
			if item.Health != HealthUnknown {
				current[index].Health = item.Health
				current[index].VendorState = item.VendorState
			}
			continue
		}
		byID[item.ID] = len(current)
		current = append(current, item)
	}
	return current
}

func parseStorCLI(data []byte, backend string) (vendorInventory, error) {
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return vendorInventory{}, fmt.Errorf("decode %s JSON: %w", backend, err)
	}
	controllersRaw, ok := lookupCase(document, "Controllers").([]any)
	if !ok {
		return vendorInventory{}, fmt.Errorf("%s JSON does not contain Controllers", backend)
	}
	inventory := newVendorInventory()
	for index, raw := range controllersRaw {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		commandStatus := mapValue(entry, "Command Status")
		controllerID := firstNonEmpty(stringValue(commandStatus, "Controller"), strconv.Itoa(index))
		status := stringValue(commandStatus, "Status")
		if status != "" && !strings.EqualFold(status, "Success") {
			continue
		}
		response := mapValue(entry, "Response Data")
		controller := Controller{ID: controllerID, Backend: backend, Health: HealthUnknown}
		if rows := rowsValue(response, "System Overview"); len(rows) > 0 {
			row := rows[0]
			controller.ID = firstNonEmpty(stringValue(row, "Ctl"), controller.ID)
			controller.Model = stringValue(row, "Model")
			controller.VendorState = stringValue(row, "Hlth")
			controller.Health = normalizeHealth(controller.VendorState)
		}
		basics := mapValue(response, "Basics")
		controller.Model = firstNonEmpty(controller.Model, stringValue(basics, "Model"), stringValue(response, "Product Name"))
		controller.Serial = firstNonEmpty(stringValue(basics, "Serial Number"), stringValue(response, "Serial Number"))
		controller.Firmware = firstNonEmpty(stringValue(basics, "FW Package Build"), stringValue(response, "FW Package Build"))
		controller.Vendor = map[string]string{"perccli": "Dell", "storcli": "Broadcom/LSI"}[backend]
		inventory.Controllers = append(inventory.Controllers, controller)
		for _, row := range rowsValue(response, "VD LIST") {
			identifier := firstNonEmpty(stringValue(row, "DG/VD"), stringValue(row, "VD"))
			vendorState := stringValue(row, "State")
			inventory.Volumes = append(inventory.Volumes, Volume{Controller: controller.ID, ID: identifier, Name: stringValue(row, "Name"), RAIDLevel: stringValue(row, "TYPE"), State: normalizeHealth(vendorState), VendorState: vendorState, SizeBytes: parseVendorSize(stringValue(row, "Size")), Backend: backend})
		}
		for _, row := range rowsValue(response, "PD LIST") {
			location := stringValue(row, "EID:Slt")
			enclosure, slot := "", location
			if left, right, found := strings.Cut(location, ":"); found {
				enclosure, slot = left, right
			}
			identifier := firstNonEmpty(stringValue(row, "DID"), location)
			vendorState := stringValue(row, "State")
			inventory.Disks = append(inventory.Disks, Disk{Controller: controller.ID, Enclosure: enclosure, Slot: slot, ID: identifier, State: normalizeHealth(vendorState), VendorState: vendorState, MediaType: stringValue(row, "Med"), Interface: stringValue(row, "Intf"), SizeBytes: parseVendorSize(stringValue(row, "Size")), Model: strings.TrimSpace(stringValue(row, "Model")), Backend: backend})
		}
	}
	return inventory, nil
}

func newVendorInventory() vendorInventory {
	return vendorInventory{Controllers: []Controller{}, Volumes: []Volume{}, Disks: []Disk{}, Incomplete: map[string]string{}}
}

func lookupCase(value map[string]any, key string) any {
	for candidate, result := range value {
		if strings.EqualFold(strings.TrimSpace(candidate), key) {
			return result
		}
	}
	return nil
}

func mapValue(value map[string]any, key string) map[string]any {
	result, _ := lookupCase(value, key).(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func rowsValue(value map[string]any, key string) []map[string]any {
	result := []map[string]any{}
	raw, _ := lookupCase(value, key).([]any)
	for _, item := range raw {
		if row, ok := item.(map[string]any); ok {
			result = append(result, row)
		}
	}
	return result
}

func stringValue(value map[string]any, key string) string {
	raw := lookupCase(value, key)
	if raw == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func normalizeHealth(value string) Health {
	compact := strings.ToLower(strings.TrimSpace(value))
	switch compact {
	case "opt", "optimal", "onln", "online", "ok", "success", "active", "ready", "rdy":
		return HealthOptimal
	case "dgrd", "degraded", "partially degraded":
		return HealthDegraded
	case "failed", "failure", "fail", "critical", "missing":
		return HealthFailed
	case "rbld", "rebuild", "rebuilding", "recovery", "resync":
		return HealthRebuilding
	case "init", "initializing":
		return HealthInitializing
	case "offln", "offline", "stopped":
		return HealthOffline
	default:
		return HealthUnknown
	}
}

func parseVendorSize(value string) uint64 {
	fields := strings.Fields(strings.ReplaceAll(value, ",", ""))
	if len(fields) == 0 {
		return 0
	}
	number, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	multiplier := float64(1)
	if len(fields) > 1 {
		switch strings.ToUpper(strings.TrimSuffix(fields[1], "B")) {
		case "K":
			multiplier = 1e3
		case "KI":
			multiplier = 1 << 10
		case "M":
			multiplier = 1e6
		case "MI":
			multiplier = 1 << 20
		case "G":
			multiplier = 1e9
		case "GI":
			multiplier = 1 << 30
		case "T":
			multiplier = 1e12
		case "TI":
			multiplier = 1 << 40
		case "P":
			multiplier = 1e15
		case "PI":
			multiplier = 1 << 50
		}
	}
	return uint64(number * multiplier)
}
