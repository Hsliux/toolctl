package raid

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/apperror"
	"toolctl/internal/core"
)

type selection struct {
	backend, controller, volume string
	abnormal                    bool
}

func parseSelection(op v1alpha1.Operation) (selection, error) {
	s := selection{}
	var e error
	if s.backend, e = stringOption(op, "backend"); e != nil {
		return s, e
	}
	s.backend = canonicalBackend(s.backend)
	if s.backend != "" && s.backend != "mdraid" {
		if _, ok := definition(s.backend); !ok {
			return s, apperror.New(v1alpha1.ErrorInvalidArgument, "unsupported RAID backend "+s.backend)
		}
	}
	if s.controller, e = stringOption(op, "controller"); e != nil {
		return s, e
	}
	if s.volume, e = stringOption(op, "volume"); e != nil {
		return s, e
	}
	if s.abnormal, e = boolOption(op, "abnormal"); e != nil {
		return s, e
	}
	return s, nil
}
func (s selection) disk(d Disk) bool {
	if s.backend != "" && s.backend != d.Backend {
		return false
	}
	if s.controller != "" && s.controller != d.Controller {
		return false
	}
	if s.volume != "" {
		found := false
		for _, v := range d.Volumes {
			if sameVolume(v, s.volume) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return !s.abnormal || abnormalDisk(d)
}
func sameVolume(a, b string) bool {
	if a == b {
		return true
	}
	_, tail, ok := strings.Cut(a, "/")
	return ok && tail == b
}
func uniqueAppend(values []string, v string) []string {
	if v == "" {
		return values
	}
	for _, old := range values {
		if old == v {
			return values
		}
	}
	return append(values, v)
}
func diskKey(d Disk) string {
	location := d.ID
	if d.Enclosure != "" && d.Slot != "" {
		location = d.Enclosure + ":" + d.Slot
	}
	return d.Backend + "/" + d.Controller + "/" + location
}

func (r *Runner) memberRows(ctx context.Context) ([]Member, core.RunOutput, error) {
	out, e := r.members(ctx)
	if e != nil {
		return nil, out, e
	}
	rows := []Member{}
	for _, item := range out.Items {
		var m Member
		if e := json.Unmarshal(item.Data, &m); e != nil {
			return nil, out, e
		}
		m.Disk.Controller = m.Controller
		rows = append(rows, m)
	}
	return rows, out, nil
}

func (r *Runner) selectedMembers(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	s, e := parseSelection(op)
	if e != nil {
		return core.RunOutput{}, e
	}
	out, e := r.membersFor(ctx, s)
	if e != nil {
		return out, e
	}
	items := out.Items[:0]
	matched := false
	for _, item := range out.Items {
		var m Member
		if e := json.Unmarshal(item.Data, &m); e != nil {
			return out, e
		}
		m.Disk.Controller = m.Controller
		m.Volumes = []string{m.Volume}
		plain := s
		plain.abnormal = false
		if plain.disk(m.Disk) {
			matched = true
		}
		if s.disk(m.Disk) {
			items = append(items, item)
		}
	}
	out.Items = items
	if !matched && len(out.Errors) == 0 && (s.backend != "" || s.controller != "" || s.volume != "") {
		out.Errors = append(out.Errors, v1alpha1.TargetError{Code: v1alpha1.ErrorInvalidArgument, Message: "no RAID member matches the requested backend/controller/volume"})
	}
	return out, nil
}

// Join member inventory to disk inventory so disappeared drives are visible
// to ordinary disk queries and onsite handoff, using vendor locations only.
func (r *Runner) enrichedInventory(ctx context.Context) (vendorInventory, []v1alpha1.Diagnostic) {
	inv, warnings := r.inventory(ctx)
	members, out, e := r.memberRows(ctx)
	warnings = append(warnings, out.Warnings...)
	if e != nil {
		inv.Incomplete["members"] = e.Error()
		warnings = append(warnings, diagnostic("RAID_MEMBERS_INCOMPLETE", e.Error(), nil))
	}
	for _, e := range out.Errors {
		key := "members"
		if e.Target != "" {
			key += "/" + e.Target
		}
		inv.Incomplete[key] = e.Message
		warnings = append(warnings, diagnostic("RAID_MEMBERS_INCOMPLETE", e.Message, nil))
	}
	indices := map[string]int{}
	unique := []Disk{}
	for _, d := range inv.Disks {
		key := diskKey(d)
		if i, ok := indices[key]; ok {
			unique[i] = mergeDiskFields(unique[i], d)
			continue
		}
		indices[key] = len(unique)
		unique = append(unique, d)
	}
	inv.Disks = unique
	controllers := []Controller{}
	seenControllers := map[string]bool{}
	for _, c := range inv.Controllers {
		key := c.Backend + "/" + c.ID
		if !seenControllers[key] {
			seenControllers[key] = true
			controllers = append(controllers, c)
		}
	}
	inv.Controllers = controllers
	volumes := []Volume{}
	seenVolumes := map[string]bool{}
	for _, v := range inv.Volumes {
		key := v.Backend + "/" + v.Controller + "/" + v.ID
		if !seenVolumes[key] {
			seenVolumes[key] = true
			volumes = append(volumes, v)
		}
	}
	inv.Volumes = volumes
	for _, m := range members {
		if m.Backend == "mdraid" {
			continue
		}
		d := m.Disk
		d.Controller = m.Controller
		key := diskKey(d)
		i, ok := indices[key]
		if !ok {
			i = len(inv.Disks)
			indices[key] = i
			inv.Disks = append(inv.Disks, d)
		}
		item := &inv.Disks[i]
		item.Volumes = uniqueAppend(item.Volumes, m.Volume)
		item.RAIDLevels = uniqueAppend(item.RAIDLevels, m.RAIDLevel)
		item.Role = "member"
		item.Serial = firstNonEmpty(item.Serial, d.Serial)
		item.Model = firstNonEmpty(item.Model, d.Model)
		for j := range inv.Volumes {
			v := &inv.Volumes[j]
			if v.Backend == m.Backend && v.Controller == m.Controller && sameVolume(v.ID, m.Volume) && m.RAIDLevel != "" {
				v.RAIDLevel = m.RAIDLevel
			}
		}
	}
	for i := range inv.Disks {
		d := &inv.Disks[i]
		if d.Role != "" {
			continue
		}
		status := physicalDiskStatus(*d)
		switch status {
		case "hotspare", "global-hotspare", "dedicated-hotspare":
			d.Role = status
		case "unconfigured-good", "unconfigured-bad", "jbod":
			d.Role = "unassigned"
		default:
			d.Role = "unknown"
		}
	}
	return inv, warnings
}

func mergeDiskFields(old, new Disk) Disk {
	if new.ID != "" {
		old.ID = new.ID
	}
	if new.Serial != "" {
		old.Serial = new.Serial
	}
	if new.Model != "" {
		old.Model = new.Model
	}
	if new.Firmware != "" {
		old.Firmware = new.Firmware
	}
	if new.SizeBytes != 0 {
		old.SizeBytes = new.SizeBytes
	}
	if new.MediaType != "" {
		old.MediaType = new.MediaType
	}
	if new.Interface != "" {
		old.Interface = new.Interface
	}
	if new.VendorState != "" {
		old.VendorState = new.VendorState
		old.State = new.State
	}
	if new.TemperatureC != nil {
		old.TemperatureC = new.TemperatureC
	}
	if new.PredictiveFailure != nil {
		old.PredictiveFailure = new.PredictiveFailure
	}
	if new.MediaErrors != nil {
		old.MediaErrors = new.MediaErrors
	}
	if new.OtherErrors != nil {
		old.OtherErrors = new.OtherErrors
	}
	return old
}

func filterInventory(inv vendorInventory, s selection) vendorInventory {
	disks := []Disk{}
	for _, d := range inv.Disks {
		if s.disk(d) {
			disks = append(disks, d)
		}
	}
	inv.Disks = disks
	volumes := []Volume{}
	for _, v := range inv.Volumes {
		if (s.backend == "" || s.backend == v.Backend) && (s.controller == "" || s.controller == v.Controller) && (s.volume == "" || sameVolume(v.ID, s.volume)) {
			volumes = append(volumes, v)
		}
	}
	inv.Volumes = volumes
	controllers := []Controller{}
	for _, c := range inv.Controllers {
		if (s.backend == "" || s.backend == c.Backend) && (s.controller == "" || s.controller == c.ID) {
			controllers = append(controllers, c)
		}
	}
	inv.Controllers = controllers
	return inv
}

func durationOption(op v1alpha1.Operation, name string, def int64) (int64, error) {
	raw, ok := op.Options[name]
	if !ok {
		return def, nil
	}
	var n int64
	if e := json.Unmarshal(raw, &n); e != nil {
		return 0, apperror.New(v1alpha1.ErrorInvalidArgument, fmt.Sprintf("invalid --%s", name))
	}
	return n, nil
}

func inventoryErrors(inv vendorInventory) []v1alpha1.TargetError {
	return inventoryErrorsFor(inv, selection{})
}
func inventoryErrorsFor(inv vendorInventory, s selection) []v1alpha1.TargetError {
	keys := []string{}
	for key := range inv.Incomplete {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := []v1alpha1.TargetError{}
	for _, key := range keys {
		backend := strings.TrimPrefix(key, "members/")
		if s.backend != "" && s.backend != backend {
			if _, known := definition(backend); known {
				continue
			}
		}
		out = append(out, v1alpha1.TargetError{Code: v1alpha1.ErrorExecutionFailed, Message: key + ": " + inv.Incomplete[key], Details: map[string]string{"source": key}})
	}
	return out
}
func selectionExists(inv vendorInventory, s selection) bool {
	if s.backend == "" && s.controller == "" && s.volume == "" {
		return true
	}
	match := func(backend, controller string) bool {
		return (s.backend == "" || s.backend == backend) && (s.controller == "" || s.controller == controller)
	}
	for _, d := range inv.Disks {
		plain := s
		plain.abnormal = false
		if plain.disk(d) {
			return true
		}
	}
	for _, v := range inv.Volumes {
		if match(v.Backend, v.Controller) && (s.volume == "" || sameVolume(v.ID, s.volume)) {
			return true
		}
	}
	if s.volume == "" {
		for _, c := range inv.Controllers {
			if match(c.Backend, c.ID) {
				return true
			}
		}
	}
	return false
}
func completeQuery(out *core.RunOutput, inv vendorInventory, s selection) {
	out.Errors = append(out.Errors, inventoryErrorsFor(inv, s)...)
	for _, c := range inv.Controllers {
		if (s.backend != "" && s.backend != c.Backend) || (s.controller != "" && s.controller != c.ID) {
			continue
		}
		if strings.HasPrefix(c.ID, "pci-") && inv.Incomplete[c.Backend] == "" {
			code := v1alpha1.ErrorExecutionFailed
			message := "vendor query did not resolve controller ID for " + c.PCIAddress
			if c.Backend == "unknown" {
				code = v1alpha1.ErrorUnsupportedCapability
				message = "unsupported RAID controller at " + c.PCIAddress
			}
			out.Errors = append(out.Errors, v1alpha1.TargetError{Code: code, Message: message})
		}
	}
	if len(out.Errors) == 0 && !selectionExists(inv, s) {
		out.Errors = append(out.Errors, v1alpha1.TargetError{Code: v1alpha1.ErrorInvalidArgument, Message: "no RAID resource matches the requested backend/controller/volume"})
	}
	if len(inv.Controllers) == 0 && len(inv.Volumes) == 0 && len(out.Errors) == 0 {
		out.Warnings = append(out.Warnings, diagnostic("RAID_NOT_DETECTED", "no hardware RAID controller or Linux MD array detected", nil))
	}
}
