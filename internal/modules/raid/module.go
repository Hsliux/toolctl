package raid

import (
	"context"
	"encoding/json"
	"fmt"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const (
	capabilityOverview    = "raid.overview"
	capabilityDoctor      = "raid.doctor"
	capabilityStatus      = "raid.status"
	capabilityControllers = "raid.controller.list"
	capabilityVolumes     = "raid.volume.list"
	capabilityDisks       = "raid.disk.list"
	capabilityInit        = "raid.init"
)

type Module struct{ deps core.Dependencies }

func New() *Module { return &Module{} }

func (*Module) Info() v1alpha1.ModuleInfo {
	return v1alpha1.ModuleInfo{Name: "raid", Version: "0.1.0", MaxConcurrency: 1}
}

func (*Module) Capabilities() []v1alpha1.Capability {
	return []v1alpha1.Capability{
		{ID: capabilityOverview, Domain: "raid", Resource: "overview", Verb: "get", Command: v1alpha1.CommandPathSpec{Path: []string{"raid"}}, Summary: "Show RAID summary and physical disks; discovers installed tools automatically", Columns: []v1alpha1.ColumnHint{
			{Header: "TYPE", Path: "data.type", Type: v1alpha1.ColumnString, Order: 10},
			{Header: "CONTROLLER", Path: "data.controller", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "ENCLOSURE", Path: "data.enclosure", Type: v1alpha1.ColumnString, Order: 30},
			{Header: "SLOT", Path: "data.slot", Type: v1alpha1.ColumnString, Order: 40},
			{Header: "DISK ID", Path: "data.id", Type: v1alpha1.ColumnString, Order: 50},
			{Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 60},
			{Header: "DETAILS", Path: "data.details", Type: v1alpha1.ColumnString, Order: 70},
		}},
		{ID: capabilityDoctor, Domain: "raid", Resource: "doctor", Verb: "check", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "doctor"}}, Summary: "Detect RAID controllers and required vendor tools", Columns: []v1alpha1.ColumnHint{
			{Header: "CONTROLLER", Path: "data.controller", Type: v1alpha1.ColumnString, Order: 10},
			{Header: "VENDOR", Path: "data.vendor", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "BACKEND", Path: "data.backend", Type: v1alpha1.ColumnString, Order: 30},
			{Header: "TOOL", Path: "data.tool", Type: v1alpha1.ColumnString, Order: 40},
			{Header: "AVAILABLE", Path: "data.available", Type: v1alpha1.ColumnBoolean, Order: 50},
			{Header: "REGISTERED", Path: "data.registered", Type: v1alpha1.ColumnBoolean, Order: 60},
			{Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 70},
			{Header: "MESSAGE", Path: "data.message", Type: v1alpha1.ColumnString, Order: 80},
			{Header: "PATH", Path: "data.toolPath", Type: v1alpha1.ColumnString, Wide: true, Order: 90},
			{Header: "SCOPE", Path: "data.scope", Type: v1alpha1.ColumnString, Wide: true, Order: 100},
			{Header: "CONFIG", Path: "data.configPath", Type: v1alpha1.ColumnString, Wide: true, Order: 110},
		}},
		{ID: capabilityStatus, Domain: "raid", Resource: "status", Verb: "get", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "status"}}, Summary: "Show aggregate RAID health", Columns: []v1alpha1.ColumnHint{
			{Header: "HEALTH", Path: "data.health", Type: v1alpha1.ColumnString, Order: 10},
			{Header: "COMPLETE", Path: "data.inventoryComplete", Type: v1alpha1.ColumnBoolean, Order: 15},
			{Header: "CONTROLLERS", Path: "data.controllers", Type: v1alpha1.ColumnInteger, Order: 20},
			{Header: "VOLUMES", Path: "data.volumes", Type: v1alpha1.ColumnInteger, Order: 30},
			{Header: "DISKS", Path: "data.disks", Type: v1alpha1.ColumnInteger, Order: 40},
			{Header: "DEGRADED", Path: "data.degraded", Type: v1alpha1.ColumnInteger, Order: 50},
			{Header: "FAILED", Path: "data.failed", Type: v1alpha1.ColumnInteger, Order: 60},
			{Header: "REBUILDING", Path: "data.rebuilding", Type: v1alpha1.ColumnInteger, Order: 70},
			{Header: "BACKENDS", Path: "data.backends", Type: v1alpha1.ColumnString, Order: 80},
			{Header: "INCOMPLETE", Path: "data.incompleteBackends", Type: v1alpha1.ColumnString, Wide: true, Order: 90},
		}},
		{ID: capabilityControllers, Domain: "raid", Resource: "controller", Verb: "list", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "controllers"}}, Summary: "List RAID controllers", Columns: []v1alpha1.ColumnHint{
			{Header: "ID", Path: "data.id", Type: v1alpha1.ColumnString, Order: 10}, {Header: "BACKEND", Path: "data.backend", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "VENDOR", Path: "data.vendor", Type: v1alpha1.ColumnString, Order: 30}, {Header: "MODEL", Path: "data.model", Type: v1alpha1.ColumnString, Order: 40},
			{Header: "HEALTH", Path: "data.health", Type: v1alpha1.ColumnString, Order: 50}, {Header: "PCI", Path: "data.pciAddress", Type: v1alpha1.ColumnString, Wide: true, Order: 60},
			{Header: "FIRMWARE", Path: "data.firmware", Type: v1alpha1.ColumnString, Wide: true, Order: 70},
		}},
		{ID: capabilityVolumes, Domain: "raid", Resource: "volume", Verb: "list", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "volumes"}}, Summary: "List RAID virtual drives and Linux MD arrays", Columns: []v1alpha1.ColumnHint{
			{Header: "CONTROLLER", Path: "data.controller", Type: v1alpha1.ColumnString, Order: 10}, {Header: "ID", Path: "data.id", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "RAID", Path: "data.raidLevel", Type: v1alpha1.ColumnString, Order: 30}, {Header: "STATE", Path: "data.state", Type: v1alpha1.ColumnString, Order: 40},
			{Header: "SIZE", Path: "data.sizeBytes", Type: v1alpha1.ColumnBytes, Order: 50}, {Header: "NAME", Path: "data.name", Type: v1alpha1.ColumnString, Order: 60},
			{Header: "DEVICE", Path: "data.devicePath", Type: v1alpha1.ColumnString, Wide: true, Order: 70}, {Header: "BACKEND", Path: "data.backend", Type: v1alpha1.ColumnString, Wide: true, Order: 80},
		}},
		{ID: capabilityDisks, Domain: "raid", Resource: "disk", Verb: "list", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "disks"}}, Summary: "List physical RAID disks", Columns: []v1alpha1.ColumnHint{
			{Header: "CONTROLLER", Path: "data.controller", Type: v1alpha1.ColumnString, Order: 10}, {Header: "ENCLOSURE", Path: "data.enclosure", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "SLOT", Path: "data.slot", Type: v1alpha1.ColumnString, Order: 30}, {Header: "STATUS", Path: "data.status", Type: v1alpha1.ColumnString, Order: 40},
			{Header: "DISK ID", Path: "data.id", Type: v1alpha1.ColumnString, Order: 35},
			{Header: "RAW STATE", Path: "data.vendorState", Type: v1alpha1.ColumnString, Order: 45},
			{Header: "SIZE", Path: "data.sizeBytes", Type: v1alpha1.ColumnBytes, Order: 50}, {Header: "MEDIA", Path: "data.mediaType", Type: v1alpha1.ColumnString, Order: 60},
			{Header: "INTERFACE", Path: "data.interface", Type: v1alpha1.ColumnString, Wide: true, Order: 70}, {Header: "MODEL", Path: "data.model", Type: v1alpha1.ColumnString, Order: 80},
			{Header: "SERIAL", Path: "data.serial", Type: v1alpha1.ColumnString, Wide: true, Order: 85},
			{Header: "ID", Path: "data.id", Type: v1alpha1.ColumnString, Wide: true, Order: 90}, {Header: "BACKEND", Path: "data.backend", Type: v1alpha1.ColumnString, Wide: true, Order: 100},
		}},
		{ID: capabilityInit, Domain: "raid", Resource: "configuration", Verb: "init", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "setup"}, Aliases: [][]string{{"init", "raid"}}}, Summary: "Save an installed tool path (optional; does not initialize RAID arrays)", Mutating: true, Options: []v1alpha1.OptionSpec{
			{Name: "backend", Type: v1alpha1.OptionString, Description: "Backend to register: storcli, perccli, ssacli, or arcconf"},
			{Name: "path", Type: v1alpha1.OptionString, Description: "Existing executable path; known filenames auto-select backend"},
			{Name: "system", Type: v1alpha1.OptionBool, Description: "Register in the system configuration instead of the current user configuration"},
		}, Columns: []v1alpha1.ColumnHint{
			{Header: "BACKEND", Path: "data.backend", Type: v1alpha1.ColumnString, Order: 10}, {Header: "PATH", Path: "data.path", Type: v1alpha1.ColumnString, Order: 20},
			{Header: "SCOPE", Path: "data.scope", Type: v1alpha1.ColumnString, Order: 30}, {Header: "CONFIG", Path: "data.configPath", Type: v1alpha1.ColumnString, Wide: true, Order: 40},
		}},
	}
}

func (m *Module) NewRunner(deps core.Dependencies) (core.Runner, error) {
	if deps.Files == nil || deps.Processes == nil {
		return nil, fmt.Errorf("filesystem and process dependencies are required")
	}
	m.deps = deps
	return &Runner{deps: deps}, nil
}

func (m *Module) Doctor(context.Context) []v1alpha1.CheckResult {
	output, err := (&Runner{deps: m.deps}).doctor()
	if err != nil {
		return []v1alpha1.CheckResult{{Name: "raid", Status: v1alpha1.CheckWarn, Message: "RAID diagnostic failed", Details: map[string]string{"reason": err.Error(), "capability": "raid"}}}
	}
	checks := []v1alpha1.CheckResult{}
	for _, item := range output.Items {
		var source DoctorCheck
		if json.Unmarshal(item.Data, &source) != nil {
			continue
		}
		status := v1alpha1.CheckWarn
		if source.Status == "ready" {
			status = v1alpha1.CheckPass
		} else if source.Status == "not-detected" {
			status = v1alpha1.CheckSkip
		}
		details := map[string]string{"capability": "raid"}
		for key, value := range map[string]string{"controller": source.Controller, "vendor": source.Vendor, "backend": source.Backend, "path": source.ToolPath, "scope": source.Scope, "config": source.ConfigPath} {
			if value != "" && value != "-" {
				details[key] = value
			}
		}
		name := "controller"
		if source.Controller != "" && source.Controller != "-" {
			name += "-" + source.Controller
		}
		checks = append(checks, v1alpha1.CheckResult{Name: name, Status: status, Message: source.Message, Suggestion: source.Suggestion, Details: details})
	}
	return checks
}

type Runner struct{ deps core.Dependencies }

func stringOption(op v1alpha1.Operation, name string) (string, error) {
	raw, ok := op.Options[name]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("invalid --%s: %w", name, err)
	}
	return value, nil
}

func boolOption(op v1alpha1.Operation, name string) (bool, error) {
	raw, ok := op.Options[name]
	if !ok {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("invalid --%s: %w", name, err)
	}
	return value, nil
}
