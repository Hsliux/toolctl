package raid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/apperror"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

func storResponse(data []byte) ([]map[string]any, error) {
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if e := dec.Decode(&doc); e != nil {
		return nil, e
	}
	entries, ok := lookupCase(doc, "Controllers").([]any)
	if !ok {
		return nil, fmt.Errorf("missing Controllers")
	}
	out := []map[string]any{}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid controller entry")
		}
		s := mapValue(entry, "Command Status")
		if !strings.EqualFold(stringValue(s, "Status"), "success") {
			return nil, fmt.Errorf("vendor query failed: %s", stringValue(s, "Description"))
		}
		out = append(out, mapValue(entry, "Response Data"))
	}
	return out, nil
}
func walkObjects(value any, visit func(map[string]any)) {
	switch v := value.(type) {
	case map[string]any:
		visit(v)
		keys := []string{}
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkObjects(v[k], visit)
		}
	case []any:
		for _, child := range v {
			walkObjects(child, visit)
		}
	}
}
func parseJSONTasks(data []byte, controller, backend, name string) ([]Task, error) {
	responses, e := storResponse(data)
	if e != nil {
		return nil, e
	}
	rows := []Task{}
	for _, response := range responses {
		walkObjects(response, func(obj map[string]any) {
			status := stringValue(obj, "Status")
			p := firstNonEmpty(stringValue(obj, "Progress%"), stringValue(obj, "Progress %"), stringValue(obj, "Progress"))
			if p == "" && status == "" {
				return
			}
			target := firstNonEmpty(stringValue(obj, "Drive-ID"), stringValue(obj, "EID:Slt"), stringValue(obj, "VD"), stringValue(obj, "VD ID"), stringValue(obj, "DG/VD"))
			if target == "" {
				return
			}
			progress := ""
			if p != "" {
				progress = progressPercent(p)
				if progress == "" {
					progress = progressPercent(p + "%")
				}
			}
			state := "unknown"
			lower := strings.ToLower(status)
			if strings.Contains(lower, "not in progress") || lower == "idle" {
				state = "idle"
			} else if progress != "" || strings.Contains(lower, "in progress") {
				state = "running"
			}
			rows = append(rows, Task{Controller: controller, Backend: backend, Target: target, Task: name, Status: state, Progress: progress, Details: status})
		})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s %s progress fields not reported", backend, name)
	}
	return rows, nil
}

type healthEvidence struct {
	Controller string `json:"controller"`
	Backend    string `json:"backend"`
	Target     string `json:"target"`
	Check      string `json:"check"`
	Status     string `json:"status"`
	Value      string `json:"value"`
}

func (r *Runner) health(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	s, e := parseSelection(op)
	if e != nil {
		return core.RunOutput{}, e
	}
	inv, warnings := r.enrichedInventory(ctx)
	inv = filterInventory(inv, s)
	out := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}
	add := func(c, b, target, check, status, value string) {
		item, e := resultbuilder.NewItem("RAIDHealthEvidence", fmt.Sprint(len(out.Items)), "", healthEvidence{c, b, target, check, status, value})
		if e == nil {
			out.Items = append(out.Items, item)
		}
	}
	for _, v := range inv.Volumes {
		state := "observed"
		if v.State == HealthUnknown || v.State == "" {
			state = "unknown"
		} else if v.State != HealthOptimal {
			state = "warn"
		}
		add(v.Controller, v.Backend, "volume "+v.ID, "volume-state", state, string(v.State))
	}
	for _, d := range inv.Disks {
		target := firstNonEmpty(physicalDiskLocation(d), d.ID)
		status := "observed"
		if abnormalDisk(d) {
			status = "warn"
		}
		add(d.Controller, d.Backend, target, "disk-state", status, physicalDiskStatus(d))
		prediction := "not-reported"
		pstate := "unknown"
		if d.PredictiveFailure != nil {
			prediction = strconv.FormatBool(*d.PredictiveFailure)
			pstate = "pass"
			if *d.PredictiveFailure {
				pstate = "warn"
			}
		}
		add(d.Controller, d.Backend, target, "predictive-failure", pstate, prediction)
		for _, field := range []struct {
			name  string
			value *uint64
		}{{"media-errors", d.MediaErrors}, {"other-errors", d.OtherErrors}} {
			value := "not-reported"
			state := "unknown"
			if field.value != nil {
				value = fmt.Sprint(*field.value)
				state = "pass"
				if *field.value > 0 {
					state = "warn"
				}
			}
			add(d.Controller, d.Backend, target, field.name, state, value)
		}
		value := "not-reported"
		if d.TemperatureC != nil {
			value = fmt.Sprintf("%.1f C", *d.TemperatureC)
		}
		state := "unknown"
		if d.TemperatureC != nil {
			state = "observed"
		}
		add(d.Controller, d.Backend, target, "temperature", state, value)
	}
	configured := toolSourcePaths(loadConfiguredToolSources(r.deps))
	seen := map[string]bool{}
	for _, c := range inv.Controllers {
		key := c.Backend + "/" + c.ID
		if seen[key] || !numericDiskAddress.MatchString(c.ID) {
			continue
		}
		seen[key] = true
		binary, _ := discoverTool(r.deps.Processes, c.Backend, configured)
		queries := [][]string{}
		switch c.Backend {
		case "megacli":
			queries = [][]string{{"-AdpBbuCmd", "-GetBbuStatus", "-a" + c.ID, "-NoLog"}, {"-LDInfo", "-Lall", "-a" + c.ID, "-NoLog"}}
		case "storcli", "perccli":
			queries = [][]string{{"/c" + c.ID + "/bbu", "show", "all", "J"}, {"/c" + c.ID + "/cv", "show", "all", "J"}, {"/c" + c.ID + "/vall", "show", "all", "J"}}
		case "arcconf":
			queries = [][]string{{"GETCONFIG", c.ID, "AL", "nologs"}}
		case "ssacli":
			queries = [][]string{{"ctrl", "slot=" + c.ID, "show", "config", "detail"}}
		}
		for _, args := range queries {
			data, e := r.memberQuery(ctx, binary, args)
			if e != nil {
				add(c.ID, c.Backend, strings.Join(args, " "), "cache-protection-query", "unavailable", e.Error())
				out.Warnings = append(out.Warnings, diagnostic("RAID_HEALTH_SOURCE_UNAVAILABLE", e.Error(), nil))
				continue
			}
			evidence := parseHealthFields(data, c.Backend)
			if len(evidence) == 0 {
				add(c.ID, c.Backend, "controller", "cache-protection", "unknown", "no cache/battery evidence recognized")
			}
			for _, field := range evidence {
				add(c.ID, c.Backend, field.target, field.key, evidenceStatus(field.key, field.value), field.value)
			}
		}
	}
	completeQuery(&out, inv, s)
	return out, nil
}

type evidenceField struct{ target, key, value string }

func evidenceStatus(key, value string) string {
	k, v := strings.ToLower(key), strings.ToLower(strings.TrimSpace(value))
	if strings.Contains(k, "replacement required") && v == "yes" {
		return "warn"
	}
	if v == "failed" || v == "failure" || v == "degraded" || v == "not ok" {
		return "warn"
	}
	if v == "optimal" || v == "ok" {
		return "pass"
	}
	return "observed"
}

func healthKey(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "cache") || strings.Contains(lower, "battery") || strings.Contains(lower, "capacitor") || strings.Contains(lower, "bbu")
}
func parseHealthFields(data []byte, backend string) []evidenceField {
	fields := []evidenceField{}
	if backend == "storcli" || backend == "perccli" {
		responses, e := storResponse(data)
		if e != nil {
			return nil
		}
		for _, response := range responses {
			var visit func(any, string)
			visit = func(value any, path string) {
				switch v := value.(type) {
				case map[string]any:
					keys := []string{}
					for k := range v {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, key := range keys {
						visit(v[key], path+"/"+key)
					}
				case []any:
					for i, child := range v {
						visit(child, fmt.Sprintf("%s/%d", path, i))
					}
				default:
					if healthKey(path) {
						fields = append(fields, evidenceField{"controller", path, fmt.Sprint(v)})
					}
				}
			}
			visit(response, "")
		}
		return fields
	}
	target := "controller"
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if m := megaVirtualDrive.FindStringSubmatch(line); len(m) == 2 {
			target = "volume " + m[1]
		}
		if m := arcLogicalID.FindStringSubmatch(line); len(m) == 2 {
			target = "volume " + m[1]
		}
		key, value, ok := splitVendorField(line)
		if ok && value != "" && healthKey(key) {
			fields = append(fields, evidenceField{target, key, value})
		}
	}
	return fields
}

type eventRow struct {
	Source     string  `json:"source"`
	Controller string  `json:"controller"`
	Backend    string  `json:"backend"`
	Time       string  `json:"time,omitempty"`
	Severity   string  `json:"severity,omitempty"`
	Message    string  `json:"message"`
	Sequence   *uint64 `json:"sequence,omitempty"`
}

var eventTime = regexp.MustCompile(`(?:[0-9]{4}[-/][0-9]{2}[-/][0-9]{2}|[0-9]{2}[-/][0-9]{2}[-/][0-9]{4})[ T][0-9]{2}:[0-9]{2}:[0-9]{2}`)

func (r *Runner) events(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	s, e := parseSelection(op)
	if e != nil {
		return core.RunOutput{}, e
	}
	count, e := durationOption(op, "count", 20)
	if e != nil {
		return core.RunOutput{}, e
	}
	if count < 1 || count > 1000 {
		return core.RunOutput{}, apperror.New(v1alpha1.ErrorInvalidArgument, "count must be between 1 and 1000")
	}
	inv, warnings := r.inventory(ctx)
	out := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}
	configured := toolSourcePaths(loadConfiguredToolSources(r.deps))
	seen := map[string]bool{}
	for _, v := range inv.Volumes {
		if v.Backend == "mdraid" && (s.backend == "" || s.backend == "mdraid") && (s.controller == "" || s.controller == "mdraid") {
			inv.Controllers = append(inv.Controllers, Controller{ID: "mdraid", Backend: "mdraid"})
			break
		}
	}
	for _, c := range inv.Controllers {
		if (s.backend != "" && s.backend != c.Backend) || (s.controller != "" && s.controller != c.ID) {
			continue
		}
		key := c.Backend + "/" + c.ID
		if seen[key] || (!numericDiskAddress.MatchString(c.ID) && c.Backend != "mdraid") {
			continue
		}
		seen[key] = true
		binary, _ := discoverTool(r.deps.Processes, c.Backend, configured)
		var rows []eventRow
		var e error
		if c.Backend == "ssacli" || c.Backend == "mdraid" {
			var notes []v1alpha1.Diagnostic
			rows, notes, e = r.additionalEvents(ctx, c, binary, inv)
			out.Warnings = append(out.Warnings, notes...)
		} else {
			var data []byte
			data, e = r.controllerEvents(ctx, c, binary, int(count))
			if e == nil {
				rows = parseEvents(data, c.ID, c.Backend)
				for i := range rows {
					rows[i].Source = "controller"
				}
			}
		}
		if e != nil {
			out.Errors = append(out.Errors, v1alpha1.TargetError{Code: v1alpha1.ErrorExecutionFailed, Message: e.Error()})
			continue
		}
		if len(rows) == 0 {
			out.Warnings = append(out.Warnings, diagnostic("RAID_EVENTS_NOT_REPORTED", "no recognizable event entries in "+c.Backend+" output", nil))
			continue
		}
		useSequence, useTime := true, true
		for _, row := range rows {
			if row.Sequence == nil {
				useSequence = false
			}
			if eventStamp(row.Time).IsZero() {
				useTime = false
			}
		}
		if useSequence {
			sort.SliceStable(rows, func(i, j int) bool { return *rows[i].Sequence > *rows[j].Sequence })
		} else if useTime {
			sort.SliceStable(rows, func(i, j int) bool { return eventStamp(rows[i].Time).After(eventStamp(rows[j].Time)) })
		}
		if !useSequence && !useTime {
			out.Warnings = append(out.Warnings, diagnostic("RAID_EVENT_ORDER_UNKNOWN", "some events lack a recognized sequence/timestamp; their recency cannot be confirmed", nil))
		}
		if len(rows) > int(count) {
			rows = rows[:count]
		}
		for i, row := range rows {
			item, e := resultbuilder.NewItem("RAIDEvent", key+"/"+fmt.Sprint(i), "", row)
			if e != nil {
				return out, e
			}
			out.Items = append(out.Items, item)
		}
	}
	completeQuery(&out, inv, s)
	return out, nil
}
func (r *Runner) controllerEvents(ctx context.Context, c Controller, binary string, count int) ([]byte, error) {
	if c.Backend == "arcconf" {
		return r.memberQuery(ctx, binary, []string{"GETLOGS", c.ID, "EVENT", "tabular", "nologs"})
	}
	if c.Backend != "megacli" && c.Backend != "storcli" && c.Backend != "perccli" {
		return nil, fmt.Errorf("event query unsupported for %s", c.Backend)
	}
	dir, e := os.MkdirTemp("", "toolctl-raid-events-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "events.log")
	args := []string{"-AdpEventLog", "-GetEvents", "-f", path, "-a" + c.ID, "-NoLog"}
	if c.Backend != "megacli" {
		args = []string{"/c" + c.ID, "show", "events", "type=latest=" + fmt.Sprint(count), "file=" + path}
	}
	data, e := r.memberQuery(ctx, binary, args)
	if e != nil {
		return nil, e
	}
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		return data, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	data, e = io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("event log exceeds 16 MiB limit")
	}
	return data, nil
}
func parseEvents(data []byte, controller, backend string) []eventRow {
	rows := []eventRow{}
	current := eventRow{Controller: controller, Backend: backend}
	flush := func() {
		if current.Message != "" {
			rows = append(rows, current)
		}
		current = eventRow{Controller: controller, Backend: backend}
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		key, value, ok := splitVendorField(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "seqnum", "sequence number", "event number":
			flush()
			v := strings.TrimSpace(value)
			base := 10
			if strings.HasPrefix(v, "0x") {
				base = 0
			}
			if n, e := strconv.ParseUint(v, base, 64); e == nil {
				current.Sequence = &n
			}
		case "time", "time stamp", "timestamp", "date/time":
			current.Time = value
		case "class", "severity":
			current.Severity = value
		case "description", "event description", "message":
			current.Message = value
		}
	}
	flush()
	// ARCCONF tabular logs commonly place one timestamped event per line.
	if len(rows) == 0 && backend == "arcconf" {
		for _, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(raw)
			if eventTime.MatchString(line) {
				rows = append(rows, eventRow{Controller: controller, Backend: backend, Message: line, Time: eventTime.FindString(line)})
			}
		}
	}
	return rows
}

func eventStamp(value string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006/01/02 15:04:05", "01/02/2006 15:04:05", "Mon Jan _2 15:04:05 2006", time.ANSIC} {
		if t, e := time.Parse(layout, value); e == nil {
			return t
		}
	}
	return time.Time{}
}
