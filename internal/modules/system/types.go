package system

type HostInfo struct {
	Hostname          string                 `json:"hostname"`
	Hardware          HardwareInfo           `json:"hardware"`
	ServerType        ServerTypeInfo         `json:"serverType"`
	OS                OSInfo                 `json:"os"`
	Kernel            KernelInfo             `json:"kernel"`
	CPU               CPUInfo                `json:"cpu"`
	Memory            MemoryInfo             `json:"memory"`
	RootFilesystem    FilesystemInfo         `json:"rootFilesystem"`
	BlockDevices      []BlockDeviceInfo      `json:"blockDevices"`
	Disks             []BlockDeviceInfo      `json:"disks"`
	DiskCount         *int                   `json:"diskCount"`
	NetworkInterfaces []NetworkInterfaceInfo `json:"networkInterfaces"`
	NICCount          *int                   `json:"nicCount"`
	UptimeSeconds     *float64               `json:"uptimeSeconds"`
}

type HardwareInfo struct {
	Vendor      string `json:"vendor,omitempty"`
	Model       string `json:"model,omitempty"`
	ChassisType string `json:"chassisType,omitempty"`
}

type ServerTypeInfo struct {
	Type           string   `json:"type"`
	Virtualization string   `json:"virtualization,omitempty"`
	Confidence     string   `json:"confidence"`
	Evidence       []string `json:"evidence"`
}

type OSInfo struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	Version    string `json:"version,omitempty"`
	PrettyName string `json:"prettyName,omitempty"`
}

type KernelInfo struct {
	Release string `json:"release,omitempty"`
}

type CPUInfo struct {
	Architecture  string `json:"architecture"`
	Model         string `json:"model,omitempty"`
	Sockets       int    `json:"sockets,omitempty"`
	PhysicalCores int    `json:"physicalCores,omitempty"`
	LogicalCPUs   int    `json:"logicalCPUs"`
}

type MemoryInfo struct {
	TotalBytes          *uint64 `json:"totalBytes"`
	AvailableBytes      *uint64 `json:"availableBytes"`
	CgroupLimitBytes    *uint64 `json:"cgroupLimitBytes"`
	EffectiveTotalBytes *uint64 `json:"effectiveTotalBytes"`
}

type FilesystemInfo struct {
	Mountpoint     string  `json:"mountpoint"`
	FilesystemType string  `json:"filesystemType,omitempty"`
	TotalBytes     *uint64 `json:"totalBytes"`
	UsedBytes      *uint64 `json:"usedBytes"`
	AvailableBytes *uint64 `json:"availableBytes"`
}

type BlockDeviceInfo struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Model      string `json:"model,omitempty"`
	SizeBytes  uint64 `json:"sizeBytes"`
	Rotational *bool  `json:"rotational,omitempty"`
	Removable  bool   `json:"removable"`
}

type NetworkInterfaceInfo struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	State     string `json:"state,omitempty"`
	MTU       *int64 `json:"mtu"`
	SpeedMbps *int64 `json:"speedMbps"`
	Duplex    string `json:"duplex,omitempty"`
	Carrier   *bool  `json:"carrier"`
	Virtual   bool   `json:"virtual"`
}
