package raid

type Health string

const (
	HealthOptimal      Health = "optimal"
	HealthDegraded     Health = "degraded"
	HealthFailed       Health = "failed"
	HealthRebuilding   Health = "rebuilding"
	HealthInitializing Health = "initializing"
	HealthOffline      Health = "offline"
	HealthUnknown      Health = "unknown"
)

type Controller struct {
	ID          string `json:"id"`
	Backend     string `json:"backend"`
	Vendor      string `json:"vendor,omitempty"`
	Model       string `json:"model,omitempty"`
	Serial      string `json:"serial,omitempty"`
	Firmware    string `json:"firmware,omitempty"`
	Driver      string `json:"driver,omitempty"`
	PCIAddress  string `json:"pciAddress,omitempty"`
	Health      Health `json:"health"`
	VendorState string `json:"vendorState,omitempty"`
}

type Volume struct {
	Controller  string `json:"controller"`
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	RAIDLevel   string `json:"raidLevel,omitempty"`
	State       Health `json:"state"`
	VendorState string `json:"vendorState,omitempty"`
	SizeBytes   uint64 `json:"sizeBytes,omitempty"`
	DevicePath  string `json:"devicePath,omitempty"`
	Backend     string `json:"backend"`
}

type Disk struct {
	Status            string   `json:"status"`
	Controller        string   `json:"controller"`
	Enclosure         string   `json:"enclosure,omitempty"`
	Slot              string   `json:"slot,omitempty"`
	ID                string   `json:"id"`
	State             Health   `json:"state"`
	VendorState       string   `json:"vendorState,omitempty"`
	MediaType         string   `json:"mediaType,omitempty"`
	Interface         string   `json:"interface,omitempty"`
	SizeBytes         uint64   `json:"sizeBytes,omitempty"`
	Model             string   `json:"model,omitempty"`
	Serial            string   `json:"serial,omitempty"`
	Firmware          string   `json:"firmware,omitempty"`
	TemperatureC      *float64 `json:"temperatureC,omitempty"`
	PredictiveFailure *bool    `json:"predictiveFailure,omitempty"`
	RebuildPercent    *float64 `json:"rebuildPercent,omitempty"`
	Backend           string   `json:"backend"`
}

type DoctorCheck struct {
	Controller   string `json:"controller"`
	Vendor       string `json:"vendor"`
	Backend      string `json:"backend"`
	Tool         string `json:"tool,omitempty"`
	ToolPath     string `json:"toolPath,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ConfigPath   string `json:"configPath,omitempty"`
	Registered   bool   `json:"registered"`
	Available    bool   `json:"available"`
	Status       string `json:"status"`
	Message      string `json:"message"`
	Suggestion   string `json:"suggestion,omitempty"`
	Architecture string `json:"architecture"`
}

type Status struct {
	Health             Health `json:"health"`
	InventoryComplete  bool   `json:"inventoryComplete"`
	IncompleteBackends string `json:"incompleteBackends,omitempty"`
	Controllers        int    `json:"controllers"`
	Volumes            int    `json:"volumes"`
	Disks              int    `json:"disks"`
	Degraded           int    `json:"degraded"`
	Failed             int    `json:"failed"`
	Rebuilding         int    `json:"rebuilding"`
	Backends           string `json:"backends,omitempty"`
}

type ToolRegistration struct {
	Backend    string `json:"backend"`
	Path       string `json:"path"`
	Scope      string `json:"scope,omitempty"`
	ConfigPath string `json:"configPath,omitempty"`
}
