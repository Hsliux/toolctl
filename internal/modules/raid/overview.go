package raid

import (
	"fmt"
	"sort"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

func overviewOutput(inv vendorInventory, warnings []v1alpha1.Diagnostic) (core.RunOutput, error) {
	out := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}
	summary := summarize(inv)
	status := string(summary.Health)
	if summary.InventoryComplete && summary.Controllers == 0 && summary.Volumes == 0 && summary.Disks == 0 {
		status = "not-detected"
	}
	item, err := resultbuilder.NewItem("RAIDOverview", "summary", "", map[string]any{
		"type": "summary", "status": status,
		"details": fmt.Sprintf("controllers=%d volumes=%d disks=%d complete=%t", summary.Controllers, summary.Volumes, summary.Disks, summary.InventoryComplete),
		"summary": summary,
	})
	if err != nil {
		return out, err
	}
	out.Items = append(out.Items, item)
	disks := append([]Disk(nil), inv.Disks...)
	sort.SliceStable(disks, func(i, j int) bool { return diskLess(disks[i], disks[j]) })
	for _, d := range disks {
		d.Status = physicalDiskStatus(d)
		item, err := resultbuilder.NewItem("RAIDPhysicalDisk", d.ID, "", map[string]any{
			"type": "disk", "controller": d.Controller, "enclosure": d.Enclosure, "slot": d.Slot, "id": d.ID, "status": d.Status,
			"details": fmt.Sprintf("%s; raw=%s; backend=%s", d.Model, d.VendorState, d.Backend), "disk": d,
		})
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
