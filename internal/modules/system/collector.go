package system

import (
	"bufio"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const maxPseudoFile = 4 << 20

func collectHost(deps core.Dependencies) (HostInfo, []v1alpha1.Diagnostic) {
	warnings := []v1alpha1.Diagnostic{}
	hostname, err := deps.Hostname()
	if err != nil {
		warnings = append(warnings, warning("HOSTNAME_UNAVAILABLE", "hostname could not be read", err))
	}
	osInfo, err := collectOS(deps.Files)
	if err != nil {
		warnings = append(warnings, warning("OS_RELEASE_UNAVAILABLE", "Linux release information could not be read", err))
	}
	kernel, err := readTrimmed(deps.Files, "/proc/sys/kernel/osrelease")
	if err != nil {
		warnings = append(warnings, warning("KERNEL_UNAVAILABLE", "kernel release could not be read", err))
	}
	cpu, cpuText, err := collectCPU(deps)
	if err != nil {
		warnings = append(warnings, warning("CPU_PARTIAL", "CPU details are incomplete", err))
	}
	memory, err := collectMemory(deps.Files)
	if err != nil {
		warnings = append(warnings, warning("MEMORY_PARTIAL", "memory details are incomplete", err))
	}
	hardware := collectHardware(deps.Files)
	serverType := detectServerType(deps.Files, hardware, cpuText, deps.OperatingSystem)
	if serverType.Type == "unknown" {
		warnings = append(warnings, v1alpha1.Diagnostic{
			Code: "SERVER_TYPE_UNKNOWN", Message: "server type could not be determined reliably", Details: map[string]string{},
		})
	}
	rootFS, err := collectRootFilesystem(deps.Files)
	if err != nil {
		warnings = append(warnings, warning("ROOT_FILESYSTEM_PARTIAL", "root filesystem details are incomplete", err))
	}
	devices, err := collectBlockDevices(deps.Files)
	devicesAvailable := err == nil
	if err != nil {
		warnings = append(warnings, warning("BLOCK_DEVICES_UNAVAILABLE", "block devices could not be read", err))
	}
	disks := topLevelDisks(devices)
	networkInterfaces, err := collectNetworkInterfaces(deps.Files)
	networkInterfacesAvailable := err == nil
	if err != nil {
		warnings = append(warnings, warning("NETWORK_INTERFACES_UNAVAILABLE", "network interfaces could not be read", err))
	}
	uptime, err := collectUptime(deps.Files)
	uptimeAvailable := err == nil
	if err != nil {
		warnings = append(warnings, warning("UPTIME_UNAVAILABLE", "uptime could not be read", err))
	}
	if devices == nil {
		devices = []BlockDeviceInfo{}
	}
	if disks == nil {
		disks = []BlockDeviceInfo{}
	}
	if networkInterfaces == nil {
		networkInterfaces = []NetworkInterfaceInfo{}
	}
	nicCount := 0
	for _, networkInterface := range networkInterfaces {
		if networkInterface.Kind != "loopback" {
			nicCount++
		}
	}
	return HostInfo{
		Hostname:          hostname,
		Hardware:          hardware,
		ServerType:        serverType,
		OS:                osInfo,
		Kernel:            KernelInfo{Release: kernel},
		CPU:               cpu,
		Memory:            memory,
		RootFilesystem:    rootFS,
		BlockDevices:      devices,
		Disks:             disks,
		DiskCount:         optionalInt(len(disks), devicesAvailable),
		NetworkInterfaces: networkInterfaces,
		NICCount:          optionalInt(nicCount, networkInterfacesAvailable),
		UptimeSeconds:     optionalFloat(uptime, uptimeAvailable),
	}, warnings
}

func warning(code, message string, err error) v1alpha1.Diagnostic {
	details := map[string]string{}
	if err != nil {
		details["reason"] = err.Error()
	}
	return v1alpha1.Diagnostic{Code: code, Message: message, Details: details}
}

func collectOS(files core.FileSystem) (OSInfo, error) {
	var lastErr error
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		data, err := files.ReadFile(path, maxPseudoFile)
		if err != nil {
			lastErr = err
			continue
		}
		values := parseKeyValues(string(data))
		return OSInfo{
			ID:         values["ID"],
			Name:       values["NAME"],
			Version:    firstNonEmpty(values["VERSION_ID"], values["VERSION"]),
			PrettyName: firstNonEmpty(values["PRETTY_NAME"], values["NAME"]),
		}, nil
	}
	return OSInfo{}, lastErr
}

func parseKeyValues(content string) map[string]string {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			} else {
				value = strings.Trim(value, "\"'")
			}
		}
		values[strings.TrimSpace(key)] = value
	}
	return values
}

func collectCPU(deps core.Dependencies) (CPUInfo, string, error) {
	data, err := deps.Files.ReadFile("/proc/cpuinfo", maxPseudoFile)
	info := CPUInfo{Architecture: deps.Architecture, LogicalCPUs: deps.LogicalCPUs}
	if err != nil {
		return info, "", err
	}
	text := string(data)
	sockets := map[string]struct{}{}
	cores := map[string]struct{}{}
	logical := 0
	physicalID := ""
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "processor":
			logical++
		case "model name", "Processor", "Hardware":
			if info.Model == "" {
				info.Model = value
			}
		case "physical id":
			physicalID = value
			sockets[value] = struct{}{}
		case "core id":
			cores[physicalID+":"+value] = struct{}{}
		}
	}
	if logical > 0 {
		info.LogicalCPUs = logical
	}
	info.Sockets = len(sockets)
	info.PhysicalCores = len(cores)
	if info.Sockets == 0 || info.PhysicalCores == 0 {
		sysfsSockets, sysfsCores := collectSysfsCPUTopology(deps.Files)
		if info.Sockets == 0 {
			info.Sockets = sysfsSockets
		}
		if info.PhysicalCores == 0 {
			info.PhysicalCores = sysfsCores
		}
	}
	return info, text, nil
}

func collectSysfsCPUTopology(files core.FileSystem) (int, int) {
	entries, err := files.ReadDir("/sys/devices/system/cpu")
	if err != nil {
		return 0, 0
	}
	sockets := map[string]struct{}{}
	cores := map[string]struct{}{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "cpu") || !decimalDigits(strings.TrimPrefix(name, "cpu")) {
			continue
		}
		base := "/sys/devices/system/cpu/" + name + "/topology/"
		packageID, packageErr := readTrimmed(files, base+"physical_package_id")
		coreID, coreErr := readTrimmed(files, base+"core_id")
		if packageErr == nil && packageID != "" {
			sockets[packageID] = struct{}{}
		}
		if coreErr == nil && coreID != "" {
			if packageID == "" {
				packageID = "0"
			}
			cores[packageID+":"+coreID] = struct{}{}
		}
	}
	return len(sockets), len(cores)
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func collectMemory(files core.FileSystem) (MemoryInfo, error) {
	data, err := files.ReadFile("/proc/meminfo", maxPseudoFile)
	if err != nil {
		return MemoryInfo{}, err
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		number, parseErr := strconv.ParseUint(fields[0], 10, 64)
		if parseErr == nil {
			values[key] = number * 1024
		}
	}
	total := values["MemTotal"]
	available, availableSet := values["MemAvailable"]
	limit := collectCgroupMemoryLimit(files)
	effective := total
	if limit > 0 && (effective == 0 || limit < effective) {
		effective = limit
	}
	if effective > 0 && available > effective {
		available = effective
	}
	if total == 0 {
		return MemoryInfo{CgroupLimitBytes: optionalUint(limit, limit > 0), EffectiveTotalBytes: optionalUint(effective, effective > 0)}, fmt.Errorf("MemTotal is missing")
	}
	return MemoryInfo{
		TotalBytes:          optionalUint(total, true),
		AvailableBytes:      optionalUint(available, availableSet),
		CgroupLimitBytes:    optionalUint(limit, limit > 0),
		EffectiveTotalBytes: optionalUint(effective, true),
	}, nil
}

func collectCgroupMemoryLimit(files core.FileSystem) uint64 {
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		value, err := readTrimmed(files, path)
		if err != nil || value == "" || value == "max" {
			continue
		}
		limit, err := strconv.ParseUint(value, 10, 64)
		if err == nil && limit < 1<<62 {
			return limit
		}
	}
	return 0
}

func collectHardware(files core.FileSystem) HardwareInfo {
	vendor, _ := readTrimmed(files, "/sys/class/dmi/id/sys_vendor")
	model, _ := readTrimmed(files, "/sys/class/dmi/id/product_name")
	chassis, _ := readTrimmed(files, "/sys/class/dmi/id/chassis_type")
	return HardwareInfo{Vendor: vendor, Model: model, ChassisType: chassisName(chassis)}
}

func chassisName(value string) string {
	switch value {
	case "17":
		return "main-server"
	case "23":
		return "rack-mount"
	case "3":
		return "desktop"
	case "8", "9", "10", "14":
		return "portable"
	default:
		return ""
	}
}

func detectServerType(files core.FileSystem, hardware HardwareInfo, cpuText, operatingSystem string) ServerTypeInfo {
	evidence := []string{}
	if operatingSystem != "" && operatingSystem != "linux" {
		return ServerTypeInfo{Type: "unknown", Confidence: "high", Evidence: []string{"unsupported-operating-system"}}
	}
	containerMarkers := []struct{ path, name string }{{"/.dockerenv", "docker"}, {"/run/.containerenv", "podman"}}
	for _, marker := range containerMarkers {
		if _, err := files.Stat(marker.path); err == nil {
			return ServerTypeInfo{Type: "container", Virtualization: marker.name, Confidence: "high", Evidence: []string{"container-marker"}}
		}
	}
	cgroup, _ := readTrimmed(files, "/proc/1/cgroup")
	lowerCgroup := strings.ToLower(cgroup)
	cgroupMarkers := []struct{ marker, name string }{{"kubepods", "kubernetes"}, {"containerd", "containerd"}, {"docker", "docker"}, {"libpod", "podman"}, {"lxc", "lxc"}}
	for _, marker := range cgroupMarkers {
		if strings.Contains(lowerCgroup, marker.marker) {
			return ServerTypeInfo{Type: "container", Virtualization: marker.name, Confidence: "high", Evidence: []string{"cgroup-container-marker"}}
		}
	}
	identity := strings.ToLower(hardware.Vendor + " " + hardware.Model)
	virtualization := ""
	virtualizationMarkers := []struct{ marker, name string }{
		{"vmware", "vmware"}, {"kvm", "kvm"}, {"qemu", "qemu"}, {"virtualbox", "virtualbox"},
		{"xen", "xen"}, {"hvm domu", "xen"}, {"microsoft corporation virtual machine", "hyper-v"},
		{"amazon ec2", "amazon-nitro"}, {"google compute engine", "google-compute-engine"},
	}
	for _, marker := range virtualizationMarkers {
		if strings.Contains(identity, marker.marker) {
			virtualization = marker.name
			evidence = append(evidence, "dmi-virtualization-marker")
			break
		}
	}
	hypervisorFlag := strings.Contains(strings.ToLower(cpuText), "hypervisor")
	if hypervisorFlag {
		evidence = append(evidence, "cpu-hypervisor-flag")
	}
	if virtualization != "" {
		confidence := "medium"
		if hypervisorFlag {
			confidence = "high"
		}
		return ServerTypeInfo{Type: "virtual-machine", Virtualization: virtualization, Confidence: confidence, Evidence: evidence}
	}
	if hypervisorFlag {
		return ServerTypeInfo{Type: "virtual-machine", Virtualization: "unknown", Confidence: "medium", Evidence: evidence}
	}
	if hardware.Vendor != "" || hardware.Model != "" {
		evidence := []string{"dmi-hardware-present"}
		if cpuText != "" {
			evidence = append(evidence, "no-hypervisor-flag")
		}
		return ServerTypeInfo{Type: "physical", Confidence: "medium", Evidence: evidence}
	}
	return ServerTypeInfo{Type: "unknown", Confidence: "low", Evidence: []string{}}
}

func collectRootFilesystem(files core.FileSystem) (FilesystemInfo, error) {
	stats, err := files.StatFS("/")
	if err != nil {
		return FilesystemInfo{Mountpoint: "/"}, err
	}
	filesystemType := ""
	if data, readErr := files.ReadFile("/proc/self/mountinfo", maxPseudoFile); readErr == nil {
		filesystemType = rootFilesystemType(string(data))
	}
	used := uint64(0)
	if stats.TotalBytes >= stats.FreeBytes {
		used = stats.TotalBytes - stats.FreeBytes
	}
	return FilesystemInfo{
		Mountpoint: "/", FilesystemType: filesystemType,
		TotalBytes: optionalUint(stats.TotalBytes, true), UsedBytes: optionalUint(used, true), AvailableBytes: optionalUint(stats.AvailableBytes, true),
	}, nil
}

func rootFilesystemType(content string) string {
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		left := strings.Fields(parts[0])
		right := strings.Fields(parts[1])
		if len(left) > 4 && left[4] == "/" && len(right) > 0 {
			return right[0]
		}
	}
	return ""
}

func collectBlockDevices(files core.FileSystem) ([]BlockDeviceInfo, error) {
	entries, err := files.ReadDir("/sys/class/block")
	if err != nil {
		return nil, err
	}
	devices := make([]BlockDeviceInfo, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram") {
			continue
		}
		base := "/sys/class/block/" + name
		sizeText, err := readTrimmed(files, base+"/size")
		if err != nil {
			continue
		}
		sectors, err := strconv.ParseUint(sizeText, 10, 64)
		if err != nil || sectors == 0 || sectors > ^uint64(0)/512 {
			continue
		}
		device := BlockDeviceInfo{Name: name, Kind: blockDeviceKind(files, base, name), SizeBytes: sectors * 512}
		device.Model, _ = readTrimmed(files, base+"/device/model")
		if rotational, err := readBoolNumber(files, base+"/queue/rotational"); err == nil {
			device.Rotational = &rotational
		}
		device.Removable, _ = readBoolNumber(files, base+"/removable")
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	return devices, nil
}

func topLevelDisks(devices []BlockDeviceInfo) []BlockDeviceInfo {
	disks := make([]BlockDeviceInfo, 0, len(devices))
	for _, device := range devices {
		if device.Kind == "disk" || device.Kind == "nvme" {
			disks = append(disks, device)
		}
	}
	return disks
}

func collectNetworkInterfaces(files core.FileSystem) ([]NetworkInterfaceInfo, error) {
	entries, err := files.ReadDir("/sys/class/net")
	if err != nil {
		return nil, err
	}
	interfaces := make([]NetworkInterfaceInfo, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		base := "/sys/class/net/" + name
		interfaceType, _ := readTrimmed(files, base+"/type")
		kind := "unknown"
		switch interfaceType {
		case "1":
			kind = "ethernet"
		case "772":
			kind = "loopback"
		}
		_, virtualErr := files.Stat("/sys/devices/virtual/net/" + name)
		virtual := virtualErr == nil
		if virtual && kind != "loopback" {
			kind = "virtual"
		}
		info := NetworkInterfaceInfo{Name: name, Kind: kind, Virtual: virtual}
		info.State, _ = readTrimmed(files, base+"/operstate")
		info.Duplex, _ = readTrimmed(files, base+"/duplex")
		if mtu, ok := readPositiveInt(files, base+"/mtu"); ok {
			info.MTU = &mtu
		}
		if speed, ok := readPositiveInt(files, base+"/speed"); ok {
			info.SpeedMbps = &speed
		}
		if carrier, err := readBoolNumber(files, base+"/carrier"); err == nil {
			info.Carrier = &carrier
		}
		interfaces = append(interfaces, info)
	}
	sort.Slice(interfaces, func(i, j int) bool { return interfaces[i].Name < interfaces[j].Name })
	return interfaces, nil
}

func readPositiveInt(files core.FileSystem, path string) (int64, bool) {
	value, err := readTrimmed(files, path)
	if err != nil {
		return 0, false
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

func blockDeviceKind(files core.FileSystem, base, name string) string {
	if _, err := files.Stat(base + "/partition"); err == nil {
		return "partition"
	}
	if strings.HasPrefix(name, "dm-") {
		return "device-mapper"
	}
	if strings.HasPrefix(name, "nvme") {
		return "nvme"
	}
	return "disk"
}

func readBoolNumber(files core.FileSystem, path string) (bool, error) {
	value, err := readTrimmed(files, path)
	if err != nil {
		return false, err
	}
	return value == "1", nil
}

func collectUptime(files core.FileSystem) (float64, error) {
	value, err := readTrimmed(files, "/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, fmt.Errorf("uptime is empty")
	}
	return strconv.ParseFloat(fields[0], 64)
}

func readTrimmed(files core.FileSystem, path string) (string, error) {
	data, err := files.ReadFile(path, maxPseudoFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func optionalUint(value uint64, available bool) *uint64 {
	if !available {
		return nil
	}
	return &value
}

func optionalFloat(value float64, available bool) *float64 {
	if !available {
		return nil
	}
	return &value
}

func optionalInt(value int, available bool) *int {
	if !available {
		return nil
	}
	return &value
}
