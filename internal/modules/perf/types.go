package perf

type DoctorInfo struct {
	Ready        bool              `json:"ready"`
	Architecture string            `json:"architecture"`
	Sources      map[string]string `json:"sources"`
}

type HistoryDoctorInfo struct {
	Ready          bool   `json:"ready"`
	SSARPath       string `json:"ssarPath,omitempty"`
	SSARVersion    string `json:"ssarVersion,omitempty"`
	ArchivePath    string `json:"archivePath"`
	ArchivePresent bool   `json:"archivePresent"`
	Architecture   string `json:"architecture"`
}

type CPUStat struct {
	Timestamp      string  `json:"timestamp"`
	CPU            string  `json:"cpu"`
	UserPercent    float64 `json:"userPercent"`
	SystemPercent  float64 `json:"systemPercent"`
	IOWaitPercent  float64 `json:"ioWaitPercent"`
	IRQPercent     float64 `json:"irqPercent"`
	SoftIRQPercent float64 `json:"softIRQPercent"`
	UtilPercent    float64 `json:"utilPercent"`
	IdlePercent    float64 `json:"idlePercent"`
	StealPercent   float64 `json:"stealPercent"`
	GuestPercent   float64 `json:"guestPercent"`
}

type DiskIOStat struct {
	Timestamp           string  `json:"timestamp"`
	Device              string  `json:"device"`
	ReadsPerSecond      float64 `json:"readsPerSecond"`
	WritesPerSecond     float64 `json:"writesPerSecond"`
	ReadBytesPerSecond  float64 `json:"readBytesPerSecond"`
	WriteBytesPerSecond float64 `json:"writeBytesPerSecond"`
	AverageRequestBytes float64 `json:"averageRequestBytes"`
	AverageWaitMillis   float64 `json:"averageWaitMillis"`
	ReadWaitMillis      float64 `json:"readWaitMillis"`
	WriteWaitMillis     float64 `json:"writeWaitMillis"`
	AverageQueueDepth   float64 `json:"averageQueueDepth"`
	UtilizationPercent  float64 `json:"utilizationPercent"`
}

type NetworkStat struct {
	Timestamp             string  `json:"timestamp"`
	Interface             string  `json:"interface"`
	ReceiveBytesPerSec    float64 `json:"receiveBytesPerSecond"`
	TransmitBytesPerSec   float64 `json:"transmitBytesPerSecond"`
	ReceivePacketsPerSec  float64 `json:"receivePacketsPerSecond"`
	TransmitPacketsPerSec float64 `json:"transmitPacketsPerSecond"`
	ErrorsPerSecond       float64 `json:"errorsPerSecond"`
	DropsPerSecond        float64 `json:"dropsPerSecond"`
}

type TCPRetransmissionStat struct {
	Timestamp              string  `json:"timestamp"`
	SegmentsOutPerSecond   float64 `json:"segmentsOutPerSecond"`
	RetransmittedPerSecond float64 `json:"retransmittedPerSecond"`
	RetransmissionPercent  float64 `json:"retransmissionPercent"`
}
