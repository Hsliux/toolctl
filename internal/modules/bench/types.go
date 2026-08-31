package bench

type DoctorInfo struct {
	Ready      bool   `json:"ready"`
	FIOPath    string `json:"fioPath,omitempty"`
	FIOVersion string `json:"fioVersion,omitempty"`
	Message    string `json:"message"`
}

type CPUBenchmarkResult struct {
	Algorithm       string  `json:"algorithm"`
	Threads         int     `json:"threads"`
	DurationMS      int64   `json:"durationMs"`
	Hashes          uint64  `json:"hashes"`
	HashesPerSecond float64 `json:"hashesPerSecond"`
	BytesProcessed  uint64  `json:"bytesProcessed"`
	BytesPerSecond  uint64  `json:"bytesPerSecond"`
	Checksum        string  `json:"checksum"`
}

type MemoryBenchmarkResult struct {
	Threads              int     `json:"threads"`
	SizeBytes            uint64  `json:"sizeBytes"`
	PhaseDurationMS      int64   `json:"phaseDurationMs"`
	DurationMS           int64   `json:"durationMs"`
	CopyBytes            uint64  `json:"copyBytes"`
	CopyBytesPerSecond   uint64  `json:"copyBytesPerSecond"`
	RandomReads          uint64  `json:"randomReads"`
	RandomReadsPerSecond float64 `json:"randomReadsPerSecond"`
	RandomReadLatencyNs  float64 `json:"randomReadLatencyNs"`
	Checksum             uint64  `json:"checksum"`
}

type NetworkBenchmarkResult struct {
	Role                   string  `json:"role"`
	Peer                   string  `json:"peer"`
	Direction              string  `json:"direction"`
	Streams                int     `json:"streams"`
	Bytes                  uint64  `json:"bytes"`
	BytesPerSecond         uint64  `json:"bytesPerSecond"`
	SentBytes              uint64  `json:"sentBytes"`
	SentBytesPerSecond     uint64  `json:"sentBytesPerSecond"`
	ReceivedBytes          uint64  `json:"receivedBytes"`
	ReceivedBytesPerSecond uint64  `json:"receivedBytesPerSecond"`
	DurationMS             int64   `json:"durationMs"`
	ConnectLatencyMS       float64 `json:"connectLatencyMs,omitempty"`
}

type NetworkLatencyResult struct {
	Peer      string  `json:"peer"`
	Count     int     `json:"count"`
	MinMS     float64 `json:"minMs"`
	AverageMS float64 `json:"averageMs"`
	P50MS     float64 `json:"p50Ms"`
	P95MS     float64 `json:"p95Ms"`
	P99MS     float64 `json:"p99Ms"`
	MaxMS     float64 `json:"maxMs"`
}

type BurnResult struct {
	Mode                 string  `json:"mode"`
	Threads              int     `json:"threads"`
	SizeBytes            uint64  `json:"sizeBytes"`
	DurationMS           int64   `json:"durationMs"`
	Hashes               uint64  `json:"hashes"`
	HashesPerSecond      float64 `json:"hashesPerSecond"`
	MemoryBytes          uint64  `json:"memoryBytes"`
	MemoryBytesPerSecond uint64  `json:"memoryBytesPerSecond"`
	Checksum             uint64  `json:"checksum"`
}

type IOMetrics struct {
	IOPS           float64 `json:"iops"`
	BytesPerSecond uint64  `json:"bytesPerSecond"`
	Bytes          uint64  `json:"bytes"`
}

type LatencyMetrics struct {
	AverageMicros float64 `json:"averageMicros"`
	P50Micros     float64 `json:"p50Micros"`
	P95Micros     float64 `json:"p95Micros"`
	P99Micros     float64 `json:"p99Micros"`
	P999Micros    float64 `json:"p999Micros"`
	MaximumMicros float64 `json:"maximumMicros"`
}

type DiskBenchmarkResult struct {
	Mode             string         `json:"mode"`
	Profile          string         `json:"profile"`
	Target           string         `json:"target"`
	Mountpoint       string         `json:"mountpoint,omitempty"`
	RootFilesystem   bool           `json:"rootFilesystem"`
	Destructive      bool           `json:"destructive"`
	File             string         `json:"file"`
	FileKept         bool           `json:"fileKept"`
	SizeBytes        uint64         `json:"sizeBytes"`
	DurationMS       int64          `json:"durationMs"`
	WarmupMS         int64          `json:"warmupMs"`
	Jobs             int            `json:"jobs"`
	QueueDepth       int            `json:"queueDepth"`
	BlockSize        string         `json:"blockSize"`
	Read             IOMetrics      `json:"read"`
	Write            IOMetrics      `json:"write"`
	Latency          LatencyMetrics `json:"latency"`
	DiskUtilPercent  float64        `json:"diskUtilPercent"`
	UserCPUPercent   float64        `json:"userCpuPercent"`
	SystemCPUPercent float64        `json:"systemCpuPercent"`
	ContextSwitches  uint64         `json:"contextSwitches"`
	FIOVersion       string         `json:"fioVersion"`
}

type DiskBenchmarkPlan struct {
	Mode             string   `json:"mode"`
	Profile          string   `json:"profile"`
	Target           string   `json:"target"`
	Mountpoint       string   `json:"mountpoint,omitempty"`
	RootFilesystem   bool     `json:"rootFilesystem"`
	File             string   `json:"file"`
	SizeBytes        uint64   `json:"sizeBytes"`
	DurationMS       int64    `json:"durationMs"`
	WarmupMS         int64    `json:"warmupMs"`
	Jobs             int      `json:"jobs"`
	QueueDepth       int      `json:"queueDepth"`
	BlockSize        string   `json:"blockSize"`
	FIOPath          string   `json:"fioPath"`
	NeedsPreparation bool     `json:"needsPreparation"`
	PrepareArguments []string `json:"prepareArguments"`
	Arguments        []string `json:"arguments"`
}

type DeviceBenchmarkPlan struct {
	Mode        string   `json:"mode"`
	Profile     string   `json:"profile"`
	Target      string   `json:"target"`
	DeviceBytes uint64   `json:"deviceBytes,omitempty"`
	SizeBytes   uint64   `json:"sizeBytes"`
	Destructive bool     `json:"destructive"`
	Confirmed   bool     `json:"confirmed"`
	FIOPath     string   `json:"fioPath"`
	Arguments   []string `json:"arguments"`
}
