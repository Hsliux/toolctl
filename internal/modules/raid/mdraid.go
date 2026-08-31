package raid

import (
	"regexp"
	"strconv"
	"strings"

	"toolctl/internal/core"
)

var mdSizePattern = regexp.MustCompile(`([0-9]+)\s+blocks`)

func collectMDRAID(files core.FileSystem) ([]Volume, error) {
	data, err := files.ReadFile("/proc/mdstat", maxRAIDPseudoFile)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	volumes := []Volume{}
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if line == "" || strings.HasPrefix(line, "Personalities") || strings.HasPrefix(line, "unused devices") || !strings.Contains(line, " : ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		name := parts[0]
		active := parts[2] == "active"
		level := ""
		for _, field := range parts[3:] {
			if strings.HasPrefix(field, "raid") || field == "linear" || field == "multipath" {
				level = field
				break
			}
		}
		detailLines := []string{}
		for next := index + 1; next < len(lines); next++ {
			candidate := strings.TrimSpace(lines[next])
			if candidate == "" || strings.HasPrefix(candidate, "unused devices") || strings.Contains(candidate, " : ") {
				break
			}
			detailLines = append(detailLines, candidate)
			index = next
		}
		detail := strings.Join(detailLines, " ")
		state := HealthOptimal
		if !active {
			state = HealthOffline
		}
		if open := strings.LastIndex(detail, "["); open >= 0 && strings.HasSuffix(detail, "]") {
			bitmap := detail[open+1 : len(detail)-1]
			if strings.Contains(bitmap, "_") {
				state = HealthDegraded
			}
		}
		if strings.Contains(detail, "recovery") || strings.Contains(detail, "resync") || strings.Contains(detail, "reshape") {
			state = HealthRebuilding
		}
		size := uint64(0)
		if match := mdSizePattern.FindStringSubmatch(detail); len(match) == 2 {
			blocks, _ := strconv.ParseUint(match[1], 10, 64)
			size = blocks * 1024
		}
		volumes = append(volumes, Volume{Controller: "mdraid", ID: name, Name: name, RAIDLevel: level, State: state, VendorState: strings.TrimSpace(parts[1] + " " + parts[2]), SizeBytes: size, DevicePath: "/dev/" + name, Backend: "mdraid"})
	}
	return volumes, nil
}

func mdRAIDPresent(files core.FileSystem) bool {
	volumes, err := collectMDRAID(files)
	return err == nil && len(volumes) > 0
}
