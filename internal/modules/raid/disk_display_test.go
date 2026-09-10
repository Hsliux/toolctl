package raid

import (
	"encoding/json"
	"testing"
)

func TestPhysicalDiskStatus(t *testing.T) {
	for raw, want := range map[string]string{"Onln": "online", "Failed": "failed", "Rbld": "rebuilding", "Offln": "offline", "UGood": "unconfigured-good", "UBad": "unconfigured-bad", "GHS": "global-hotspare", "DHS": "dedicated-hotspare", "Ready": "unconfigured-good", "JBOD": "jbod", "Missing": "missing"} {
		if got := physicalDiskStatus(Disk{VendorState: raw, State: normalizeHealth(raw)}); got != want {
			t.Errorf("%s: got %s, want %s", raw, got, want)
		}
	}
}

func TestDiskOutputOrdersSlotsAndPreservesHealth(t *testing.T) {
	inv := newVendorInventory()
	for _, slot := range []string{"10", "2", "0"} {
		inv.Disks = append(inv.Disks, Disk{Controller: "0", Enclosure: "252", Slot: slot, ID: slot, State: HealthOptimal, VendorState: "Onln", Backend: "storcli"})
	}
	out, err := (&Runner{}).inventoryOutput(capabilityDisks, inv, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"0", "2", "10"} {
		var d Disk
		if err := json.Unmarshal(out.Items[i].Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Slot != want || d.Status != "online" || d.State != HealthOptimal || d.VendorState != "Onln" {
			t.Fatalf("unexpected disk: %+v", d)
		}
	}
	if inv.Disks[0].Slot != "10" {
		t.Fatal("output sorting mutated inventory")
	}
}
