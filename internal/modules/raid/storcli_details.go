package raid

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var storDrivePath = regexp.MustCompile(`(?i)/c([0-9]+)/e([0-9]+)/s([0-9]+)`)

func mergeStorDiskDetails(response map[string]any, controller, backend string, disks *[]Disk) {
	keys := []string{}
	for key := range response {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		m := storDrivePath.FindStringSubmatch(key)
		if len(m) != 4 || m[1] != controller {
			continue
		}
		i := -1
		for n, d := range *disks {
			if d.Backend == backend && d.Controller == controller && d.Enclosure == m[2] && d.Slot == m[3] {
				i = n
				break
			}
		}
		if i < 0 {
			i = len(*disks)
			*disks = append(*disks, Disk{Backend: backend, Controller: controller, Enclosure: m[2], Slot: m[3], ID: m[2] + ":" + m[3], State: HealthUnknown})
		}
		d := &(*disks)[i]
		walkObjects(response[key], func(obj map[string]any) {
			for field, raw := range obj {
				value := strings.TrimSpace(fmt.Sprint(raw))
				switch strings.ToLower(strings.TrimSpace(field)) {
				case "did":
					d.ID = value
				case "state":
					d.VendorState = value
					d.State = normalizeHealth(value)
				case "media error count":
					d.MediaErrors = vendorUint(value)
				case "other error count":
					d.OtherErrors = vendorUint(value)
				case "predictive failure count":
					if n := vendorUint(value); n != nil {
						failed := *n > 0
						d.PredictiveFailure = &failed
					}
				case "drive temperature":
					if number := megaTemperature.FindString(value); number != "" {
						if n, e := strconv.ParseFloat(number, 64); e == nil {
							d.TemperatureC = &n
						}
					}
				case "sn", "serial number":
					d.Serial = value
				case "model number", "model":
					d.Model = value
				}
			}
		})
	}
}
