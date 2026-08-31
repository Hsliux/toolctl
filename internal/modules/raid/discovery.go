package raid

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"toolctl/internal/core"
)

const maxRAIDPseudoFile = 4 << 20

type detectedController struct {
	Address string
	Vendor  string
	Device  string
	Backend string
}

func discoverControllers(files core.FileSystem) ([]detectedController, error) {
	entries, err := files.ReadDir("/sys/bus/pci/devices")
	if err != nil {
		return nil, err
	}
	controllers := []detectedController{}
	for _, entry := range entries {
		base := path.Join("/sys/bus/pci/devices", entry.Name())
		class, classErr := readValue(files, base+"/class")
		if classErr != nil {
			continue
		}
		class = strings.TrimPrefix(strings.ToLower(class), "0x")
		if !strings.HasPrefix(class, "0104") && !strings.HasPrefix(class, "0107") {
			continue
		}
		vendorID, _ := readValue(files, base+"/vendor")
		subsystemID, _ := readValue(files, base+"/subsystem_vendor")
		deviceID, _ := readValue(files, base+"/device")
		vendor, backend := classifyPCI(vendorID, subsystemID)
		controllers = append(controllers, detectedController{Address: entry.Name(), Vendor: vendor, Device: deviceID, Backend: backend})
	}
	sort.Slice(controllers, func(i, j int) bool { return controllers[i].Address < controllers[j].Address })
	return controllers, nil
}

func classifyPCI(vendorID, subsystemID string) (string, string) {
	clean := func(value string) string { return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x") }
	vendorID, subsystemID = clean(vendorID), clean(subsystemID)
	if subsystemID == "1028" {
		return "Dell", "perccli"
	}
	if subsystemID == "103c" {
		return "HPE", "ssacli"
	}
	switch vendorID {
	case "1000", "14e4":
		return "Broadcom/LSI", "storcli"
	case "1028":
		return "Dell", "perccli"
	case "103c":
		return "HPE", "ssacli"
	case "9005":
		return "Microchip/Adaptec", "arcconf"
	default:
		return firstNonEmpty(vendorID, "unknown"), "unknown"
	}
}

func readValue(files core.FileSystem, name string) (string, error) {
	data, err := files.ReadFile(name, maxRAIDPseudoFile)
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

func nativeControllers(files core.FileSystem) ([]Controller, error) {
	detected, err := discoverControllers(files)
	if err != nil {
		return nil, err
	}
	result := make([]Controller, 0, len(detected))
	for index, item := range detected {
		result = append(result, Controller{ID: fmt.Sprintf("pci-%d", index), Backend: item.Backend, Vendor: item.Vendor, PCIAddress: item.Address, Health: HealthUnknown})
	}
	return result, nil
}
