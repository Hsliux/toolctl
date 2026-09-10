package raid

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/apperror"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

func (r *Runner) Execute(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	switch op.Capability {
	case capabilityInit:
		return r.initialize(op)
	case capabilityDoctor:
		return r.doctor()
	case capabilityOverview, capabilityStatus, capabilityControllers, capabilityVolumes, capabilityDisks:
		inventory, warnings := r.inventory(ctx)
		return r.inventoryOutput(op.Capability, inventory, warnings)
	default:
		return core.RunOutput{}, fmt.Errorf("raid module does not support %s", op.Capability)
	}
}

func (r *Runner) initialize(op v1alpha1.Operation) (core.RunOutput, error) {
	backend, err := stringOption(op, "backend")
	if err != nil {
		return core.RunOutput{}, err
	}
	explicitPath, err := stringOption(op, "path")
	if err != nil {
		return core.RunOutput{}, err
	}
	backend = strings.ToLower(strings.TrimSpace(backend))
	system, err := boolOption(op, "system")
	if err != nil {
		return core.RunOutput{}, err
	}
	writeDirectory := r.deps.ConfigDirectory
	if system {
		writeDirectory = r.deps.SystemConfigDirectory
		if writeDirectory == "" {
			return core.RunOutput{}, apperror.New(v1alpha1.ErrorConfig, "system configuration directory is unavailable")
		}
	}
	if explicitPath != "" && backend == "" {
		for _, def := range toolDefinitions {
			for _, name := range def.Names {
				if strings.EqualFold(filepath.Base(explicitPath), name) {
					backend = def.Backend
				}
			}
		}
		if backend == "" {
			return core.RunOutput{}, apperror.New(v1alpha1.ErrorInvalidArgument, "cannot identify tool filename; specify --backend storcli, perccli, ssacli, or arcconf")
		}
	}
	if backend != "" {
		if _, ok := definition(backend); !ok {
			return core.RunOutput{}, apperror.New(v1alpha1.ErrorInvalidArgument, fmt.Sprintf("unsupported RAID backend %q", backend))
		}
	}
	configured := loadTools(writeDirectory)
	registrations := []ToolRegistration{}
	scope := r.deps.ConfigScope
	if scope == "" {
		scope = "user"
	}
	if system {
		scope = "system"
	}
	register := func(name, candidate string) error {
		if candidate == "" {
			return fmt.Errorf("%s tool is not installed; specify an existing executable with --path", name)
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			return err
		}
		if !executable(absolute) {
			return fmt.Errorf("%s is not an executable file", absolute)
		}
		if r.deps.Architecture != "" && !architectureCompatible(absolute, r.deps.Architecture) {
			return fmt.Errorf("%s is not compatible with %s", absolute, r.deps.Architecture)
		}
		configured[name] = absolute
		registrations = append(registrations, ToolRegistration{Backend: name, Path: absolute, Scope: scope, ConfigPath: configPath(writeDirectory)})
		return nil
	}
	if backend != "" {
		candidate := explicitPath
		if candidate == "" {
			candidate, _ = discoverTool(r.deps.Processes, backend, map[string]string{})
		}
		if err := register(backend, candidate); err != nil {
			code := v1alpha1.ErrorInvalidArgument
			if explicitPath == "" {
				code = v1alpha1.ErrorDependencyMissing
			}
			return core.RunOutput{}, apperror.Wrap(code, err.Error(), err)
		}
	} else {
		for _, item := range toolDefinitions {
			candidate, _ := discoverTool(r.deps.Processes, item.Backend, map[string]string{})
			if candidate != "" {
				_ = register(item.Backend, candidate)
			}
		}
		if len(registrations) == 0 {
			return core.RunOutput{}, apperror.New(v1alpha1.ErrorDependencyMissing, "no supported local RAID management tool was found")
		}
	}
	if err := saveTools(writeDirectory, configured); err != nil {
		return core.RunOutput{}, apperror.Wrap(v1alpha1.ErrorConfig, err.Error(), err)
	}
	items := []v1alpha1.Item{}
	for _, registration := range registrations {
		item, err := resultbuilder.NewItem("RAIDToolRegistration", registration.Backend, "", registration)
		if err != nil {
			return core.RunOutput{}, err
		}
		items = append(items, item)
	}
	return core.RunOutput{Items: items, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func (r *Runner) doctor() (core.RunOutput, error) {
	sources := loadConfiguredToolSources(r.deps)
	configured := toolSourcePaths(sources)
	detected, discoveryErr := discoverControllers(r.deps.Files)
	checks := []DoctorCheck{}
	if discoveryErr == nil {
		for _, controller := range detected {
			check := DoctorCheck{Controller: controller.Address, Vendor: controller.Vendor, Backend: controller.Backend, Architecture: r.deps.Architecture}
			if controller.Backend == "unknown" {
				check.Status, check.Message, check.Suggestion = "unsupported", "RAID controller detected but its vendor is not recognized", "add a backend adapter for this PCI controller"
			} else {
				path, registered := discoverTool(r.deps.Processes, controller.Backend, configured)
				check.Tool, check.ToolPath, check.Registered, check.Available = controller.Backend, path, registered, path != ""
				if registered {
					source := sources[controller.Backend]
					check.Scope, check.ConfigPath = source.Scope, source.ConfigPath
				} else if path != "" {
					check.Scope = "auto"
				}
				if path == "" {
					check.Status, check.Message, check.Suggestion = "tool-missing", "matching vendor tool is not installed or executable", fmt.Sprintf("install %s for %s then run toolctl raid; for a custom path use toolctl raid setup --backend %s --path /path/to/tool", controller.Backend, r.deps.Architecture, controller.Backend)
				} else if !architectureCompatible(path, r.deps.Architecture) {
					check.Status, check.Available, check.Message, check.Suggestion = "unsupported-arch", false, "matching vendor tool is not compatible with this architecture", fmt.Sprintf("install an %s build of %s", r.deps.Architecture, controller.Backend)
				} else if registered {
					check.Status, check.Message = "ready", "matching vendor tool is registered and executable"
				} else {
					check.Status, check.Message = "ready", "matching vendor tool is automatically available; no setup required"
				}
			}
			checks = append(checks, check)
		}
	}
	if mdRAIDPresent(r.deps.Files) {
		checks = append(checks, DoctorCheck{Controller: "mdraid", Vendor: "Linux", Backend: "mdraid", Tool: "procfs/sysfs", Scope: "builtin", Available: true, Registered: true, Status: "ready", Message: "Linux software RAID is available without an external tool", Architecture: r.deps.Architecture})
	}
	if len(checks) == 0 {
		message := "no RAID controller or active Linux MD array was detected"
		if discoveryErr != nil {
			message = "PCI RAID discovery is unavailable: " + discoveryErr.Error()
		}
		checks = append(checks, DoctorCheck{Controller: "-", Vendor: "-", Backend: "none", Status: "not-detected", Message: message, Architecture: r.deps.Architecture})
	}
	items := []v1alpha1.Item{}
	for _, check := range checks {
		item, err := resultbuilder.NewItem("RAIDDoctorCheck", check.Controller, "", check)
		if err != nil {
			return core.RunOutput{}, err
		}
		items = append(items, item)
	}
	return core.RunOutput{Items: items, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}, nil
}

func (r *Runner) inventory(ctx context.Context) (vendorInventory, []v1alpha1.Diagnostic) {
	inventory := newVendorInventory()
	warnings := []v1alpha1.Diagnostic{}
	native, nativeErr := nativeControllers(r.deps.Files)
	if nativeErr == nil {
		inventory.Controllers = append(inventory.Controllers, native...)
	} else {
		inventory.Incomplete["discovery"] = nativeErr.Error()
	}
	if volumes, err := collectMDRAID(r.deps.Files); err == nil {
		inventory.Volumes = append(inventory.Volumes, volumes...)
	}
	configured := toolSourcePaths(loadConfiguredToolSources(r.deps))
	needed := map[string]bool{}
	for _, controller := range inventory.Controllers {
		needed[controller.Backend] = true
	}
	for backend := range needed {
		if backend == "unknown" || backend == "mdraid" {
			continue
		}
		path, _ := discoverTool(r.deps.Processes, backend, configured)
		if path == "" {
			inventory.Incomplete[backend] = "management tool is unavailable"
			warnings = append(warnings, diagnostic("RAID_TOOL_MISSING", fmt.Sprintf("%s controller detected but its management tool is unavailable", backend), map[string]string{"backend": backend, "suggestion": "run toolctl raid doctor"}))
			continue
		}
		if !architectureCompatible(path, r.deps.Architecture) {
			inventory.Incomplete[backend] = "management tool architecture mismatch"
			warnings = append(warnings, diagnostic("RAID_TOOL_ARCH_MISMATCH", fmt.Sprintf("%s is not compatible with %s", path, r.deps.Architecture), map[string]string{"backend": backend, "path": path}))
			continue
		}
		var vendor vendorInventory
		var err error
		switch backend {
		case "perccli", "storcli":
			vendor, err = runStorCLI(ctx, r.deps, backend, path)
		case "ssacli":
			vendor, err = runSSACLI(ctx, r.deps, path)
		case "arcconf":
			vendor, err = runArcconf(ctx, r.deps, path)
		default:
			continue
		}
		if err != nil {
			inventory.Incomplete[backend] = err.Error()
			warnings = append(warnings, diagnostic("RAID_BACKEND_FAILED", err.Error(), map[string]string{"backend": backend, "path": path}))
			continue
		}
		inventory.Controllers = replaceBackendControllers(inventory.Controllers, backend, vendor.Controllers)
		inventory.Volumes = append(inventory.Volumes, vendor.Volumes...)
		inventory.Disks = append(inventory.Disks, vendor.Disks...)
		for name, reason := range vendor.Incomplete {
			inventory.Incomplete[name] = reason
			warnings = append(warnings, diagnostic("RAID_INVENTORY_INCOMPLETE", fmt.Sprintf("%s inventory is incomplete: %s", name, reason), map[string]string{"backend": name, "path": path}))
		}
	}
	return inventory, warnings
}

func replaceBackendControllers(current []Controller, backend string, replacements []Controller) []Controller {
	if len(replacements) == 0 {
		return current
	}
	native := []Controller{}
	result := []Controller{}
	for _, controller := range current {
		if controller.Backend != backend {
			result = append(result, controller)
		} else {
			native = append(native, controller)
		}
	}
	for index := range replacements {
		if index >= len(native) {
			break
		}
		replacements[index].PCIAddress = firstNonEmpty(replacements[index].PCIAddress, native[index].PCIAddress)
		replacements[index].Vendor = firstNonEmpty(replacements[index].Vendor, native[index].Vendor)
	}
	return append(result, replacements...)
}

func (r *Runner) inventoryOutput(capability string, inventory vendorInventory, warnings []v1alpha1.Diagnostic) (core.RunOutput, error) {
	items := []v1alpha1.Item{}
	appendItem := func(kind, name string, data any) error {
		item, err := resultbuilder.NewItem(kind, name, "", data)
		if err == nil {
			items = append(items, item)
		}
		return err
	}
	switch capability {
	case capabilityOverview:
		return overviewOutput(inventory, warnings)
	case capabilityControllers:
		for _, controller := range inventory.Controllers {
			if err := appendItem("RAIDController", controller.ID, controller); err != nil {
				return core.RunOutput{}, err
			}
		}
	case capabilityVolumes:
		for _, volume := range inventory.Volumes {
			if err := appendItem("RAIDVolume", volume.ID, volume); err != nil {
				return core.RunOutput{}, err
			}
		}
	case capabilityDisks:
		disks := append([]Disk(nil), inventory.Disks...)
		sort.SliceStable(disks, func(i, j int) bool { return diskLess(disks[i], disks[j]) })
		for _, disk := range disks {
			disk.Status = physicalDiskStatus(disk)
			if err := appendItem("RAIDPhysicalDisk", disk.ID, disk); err != nil {
				return core.RunOutput{}, err
			}
		}
	case capabilityStatus:
		status := summarize(inventory)
		if err := appendItem("RAIDStatus", "raid", status); err != nil {
			return core.RunOutput{}, err
		}
	}
	return core.RunOutput{Items: items, Warnings: warnings, Errors: []v1alpha1.TargetError{}}, nil
}

func summarize(inventory vendorInventory) Status {
	status := Status{Health: HealthUnknown, InventoryComplete: len(inventory.Incomplete) == 0, Controllers: len(inventory.Controllers), Volumes: len(inventory.Volumes), Disks: len(inventory.Disks)}
	backends := map[string]bool{}
	states := []Health{}
	for _, item := range inventory.Controllers {
		backends[item.Backend] = true
		states = append(states, item.Health)
	}
	for _, item := range inventory.Volumes {
		backends[item.Backend] = true
		states = append(states, item.State)
	}
	for _, item := range inventory.Disks {
		backends[item.Backend] = true
		states = append(states, item.State)
	}
	known := 0
	for _, state := range states {
		switch state {
		case HealthFailed, HealthOffline:
			status.Failed++
			known++
		case HealthDegraded:
			status.Degraded++
			known++
		case HealthRebuilding:
			status.Rebuilding++
			known++
		case HealthOptimal:
			known++
		}
	}
	if status.Failed > 0 {
		status.Health = HealthFailed
	} else if status.Degraded > 0 {
		status.Health = HealthDegraded
	} else if status.Rebuilding > 0 {
		status.Health = HealthRebuilding
	} else if known > 0 {
		status.Health = HealthOptimal
	}
	if !status.InventoryComplete && status.Health == HealthOptimal {
		status.Health = HealthUnknown
	}
	incomplete := make([]string, 0, len(inventory.Incomplete))
	for backend := range inventory.Incomplete {
		incomplete = append(incomplete, backend)
	}
	sort.Strings(incomplete)
	status.IncompleteBackends = strings.Join(incomplete, ",")
	names := []string{}
	for backend := range backends {
		names = append(names, backend)
	}
	sort.Strings(names)
	status.Backends = strings.Join(names, ",")
	return status
}

func diagnostic(code, message string, details map[string]string) v1alpha1.Diagnostic {
	return v1alpha1.Diagnostic{Code: code, Message: message, Details: details}
}
