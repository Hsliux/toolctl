package raid

import (
	"strconv"
	"strings"
)

// physicalDiskLocation identifies a drive within a controller. RAID tools
// report slots relative to an enclosure, so slot 0 can legitimately occur in
// more than one enclosure. Keep the vendor values unchanged and combine them
// instead of presenting the slot as a globally unique chassis bay number.
func physicalDiskLocation(d Disk) string {
	enclosure := strings.TrimSpace(d.Enclosure)
	slot := strings.TrimSpace(d.Slot)
	switch {
	case enclosure != "" && slot != "":
		return enclosure + ":" + slot
	case slot != "":
		return slot
	case enclosure != "":
		return enclosure + ":?"
	default:
		return ""
	}
}

// Physical role and readiness are separate from the aggregate health model.
func physicalDiskStatus(d Disk) string {
	state := strings.ToLower(strings.TrimSpace(d.VendorState))
	if prefix, _, ok := strings.Cut(state, ","); ok {
		state = strings.TrimSpace(prefix)
	}
	switch state {
	case "onln", "online", "ok", "optimal", "opt", "active":
		return "online"
	case "ugood", "unconfigured good", "ready", "rdy":
		return "unconfigured-good"
	case "ubad", "unconfigured bad":
		return "unconfigured-bad"
	case "ghs", "global hot spare", "global hotspare":
		return "global-hotspare"
	case "dhs", "dedicated hot spare", "dedicated hotspare":
		return "dedicated-hotspare"
	case "hsp", "hot spare", "hotspare", "spare":
		return "hotspare"
	case "jbod":
		return "jbod"
	case "missing", "msng":
		return "missing"
	case "failed", "fail", "failure":
		return "failed"
	case "offln", "offline":
		return "offline"
	case "rbld", "rebuild", "rebuilding":
		return "rebuilding"
	}
	if d.State == HealthOptimal {
		return "online"
	}
	return string(d.State)
}

func diskLess(a, b Disk) bool {
	left := []string{a.Backend, a.Controller, a.Enclosure, a.Slot, a.ID}
	right := []string{b.Backend, b.Controller, b.Enclosure, b.Slot, b.ID}
	for i, l := range left {
		r := right[i]
		if l == r {
			continue
		}
		if l == "" {
			return false
		}
		if r == "" {
			return true
		}
		ln, le := strconv.ParseUint(l, 10, 64)
		rn, re := strconv.ParseUint(r, 10, 64)
		if le == nil && re == nil && ln != rn {
			return ln < rn
		}
		return l < r
	}
	return false
}
