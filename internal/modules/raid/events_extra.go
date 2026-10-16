package raid

import (
	"archive/zip"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

const maxDiagnosticBytes = 16 << 20

func (r *Runner) additionalEvents(ctx context.Context, c Controller, binary string, inv vendorInventory) ([]eventRow, []v1alpha1.Diagnostic, error) {
	notes := []v1alpha1.Diagnostic{}
	if c.Backend == "ssacli" {
		rows, e := r.ssaDiagnosticEvents(ctx, c, binary)
		if e == nil && len(rows) > 0 {
			return rows, notes, nil
		}
		if ctx.Err() != nil {
			return nil, notes, ctx.Err()
		}
		reason := "no recognized event records in SSA diagnostic package"
		if e != nil {
			reason = e.Error()
		}
		notes = append(notes, diagnostic("RAID_EVENT_SOURCE_FALLBACK", reason+"; trying current-boot kernel logs", nil))
	}
	if c.Backend == "ssacli" && c.PCIAddress == "" {
		count := 0
		for _, candidate := range inv.Controllers {
			if candidate.Backend == "ssacli" {
				count++
			}
		}
		if count > 1 {
			return nil, notes, fmt.Errorf("cannot associate kernel events with SSA controller %s: multiple controllers and no vendor-reported PCI address", c.ID)
		}
	}
	rows, source, e := r.kernelEvents(ctx, c, inv)
	if e != nil {
		return nil, notes, e
	}
	notes = append(notes, diagnostic("RAID_KERNEL_EVENT_SOURCE", "using "+source+" current-boot kernel messages; these are not complete firmware event logs", nil))
	return rows, notes, nil
}

func (r *Runner) ssaDiagnosticEvents(ctx context.Context, c Controller, binary string) ([]eventRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dir, e := os.MkdirTemp("", "toolctl-ssa-events-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "events.zip")
	data, e := r.memberQuery(ctx, binary, []string{"ctrl", "slot=" + c.ID, "diag", "logs=on", "file=" + path})
	if e != nil {
		return nil, e
	}
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		rows := parseDiagnosticEvents(data, c)
		if len(rows) > 0 {
			return rows, nil
		}
		return nil, fmt.Errorf("SSA diagnostic command did not produce a report")
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if info.Size() > 64<<20 {
		return nil, fmt.Errorf("SSA diagnostic archive exceeds 64 MiB limit")
	}
	archive, e := zip.NewReader(f, info.Size())
	if e != nil {
		return nil, fmt.Errorf("invalid SSA diagnostic archive: %w", e)
	}
	remaining := int64(maxDiagnosticBytes)
	rows := []eventRow{}
	if len(archive.File) > 1024 {
		return nil, fmt.Errorf("SSA diagnostic archive contains too many entries")
	}
	for _, entry := range archive.File {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name)
		ext := filepath.Ext(name)
		if ext != ".json" && ext != ".xml" && !((ext == ".txt" || ext == ".log") && (strings.Contains(name, "event") || strings.Contains(name, "log"))) {
			continue
		}
		if remaining <= 0 || entry.UncompressedSize64 > uint64(remaining) {
			return nil, fmt.Errorf("SSA diagnostic text exceeds 16 MiB limit")
		}
		reader, e := entry.Open()
		if e != nil {
			return nil, e
		}
		data, e := io.ReadAll(io.LimitReader(reader, remaining+1))
		_ = reader.Close()
		if e != nil {
			return nil, e
		}
		if int64(len(data)) > remaining {
			return nil, fmt.Errorf("SSA diagnostic decompression limit exceeded")
		}
		remaining -= int64(len(data))
		rows = append(rows, parseDiagnosticEvents(data, c)...)
	}
	return uniqueEvents(rows), nil
}

func uniqueEvents(rows []eventRow) []eventRow {
	seen := map[string]bool{}
	out := []eventRow{}
	for _, row := range rows {
		sequence := ""
		if row.Sequence != nil {
			sequence = fmt.Sprint(*row.Sequence)
		}
		key := row.Source + "/" + row.Controller + "/" + sequence + "/" + row.Time + "/" + row.Message
		if !seen[key] {
			seen[key] = true
			out = append(out, row)
		}
	}
	return out
}

func eventField(key string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "_", "", "-", "").Replace(key))
}
func diagnosticEvent(fields map[string]string, c Controller) (eventRow, bool) {
	row := eventRow{Controller: c.ID, Backend: c.Backend, Source: "ssacli-diagnostic"}
	for k, v := range fields {
		switch eventField(k) {
		case "description", "eventdescription", "message":
			row.Message = v
		case "time", "timestamp", "datetime", "dateandtime":
			row.Time = v
		case "severity", "class", "level":
			row.Severity = v
		case "sequencenumber", "seqnum", "sequence":
			base := 10
			if strings.HasPrefix(strings.ToLower(v), "0x") {
				base = 0
			}
			if n, e := strconv.ParseUint(v, base, 64); e == nil {
				row.Sequence = &n
			}
		}
	}
	return row, row.Message != "" && (row.Time != "" || row.Sequence != nil)
}
func parseDiagnosticEvents(data []byte, c Controller) []eventRow {
	rows := []eventRow{}
	var doc any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if decoder.Decode(&doc) == nil {
		walkObjects(doc, func(obj map[string]any) {
			fields := map[string]string{}
			for k, v := range obj {
				switch v.(type) {
				case string, json.Number:
					fields[k] = fmt.Sprint(v)
				}
			}
			if row, ok := diagnosticEvent(fields, c); ok {
				rows = append(rows, row)
			}
		})
		return rows
	}
	if strings.HasPrefix(strings.TrimSpace(string(data)), "<") {
		dec := xml.NewDecoder(strings.NewReader(string(data)))
		depth := 0
		eventDepth := -1
		fields := map[string]string{}
		key := ""
		for {
			token, e := dec.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil
			}
			switch t := token.(type) {
			case xml.StartElement:
				depth++
				if depth > 64 {
					return nil
				}
				name := eventField(t.Name.Local)
				if eventDepth < 0 && (name == "event" || name == "eventrecord" || name == "logentry") {
					eventDepth = depth
					fields = map[string]string{}
					for _, a := range t.Attr {
						fields[a.Name.Local] = a.Value
					}
				} else if eventDepth > 0 {
					key = t.Name.Local
				}
			case xml.CharData:
				if eventDepth > 0 && key != "" {
					fields[key] += string(t)
				}
			case xml.EndElement:
				if depth == eventDepth {
					for k, v := range fields {
						fields[k] = strings.TrimSpace(v)
					}
					if row, ok := diagnosticEvent(fields, c); ok {
						rows = append(rows, row)
					}
					eventDepth = -1
				}
				key = ""
				depth--
			}
		}
		return rows
	}
	for _, row := range parseEvents(data, c.ID, c.Backend) {
		if row.Time != "" || row.Sequence != nil {
			row.Source = "ssacli-diagnostic"
			rows = append(rows, row)
		}
	}
	return rows
}

var pciToken = regexp.MustCompile(`(?i)(?:[0-9a-f]{4}:)?[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]`)
var mdToken = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(md[0-9]+|md_[A-Za-z0-9_-]+)(?:$|[^A-Za-z0-9_])`)

func kernelMatches(message string, c Controller, inv vendorInventory) bool {
	lower := strings.ToLower(message)
	if c.Backend == "mdraid" {
		if !strings.Contains(lower, "md:") && !strings.Contains(lower, "md/") && !strings.Contains(lower, "md_") && !strings.Contains(lower, "raid") {
			return false
		}
		for _, match := range mdToken.FindAllStringSubmatch(lower, -1) {
			for _, v := range inv.Volumes {
				if v.Backend == "mdraid" && strings.EqualFold(v.ID, match[1]) {
					return true
				}
			}
		}
		return false
	}
	addresses := pciToken.FindAllString(lower, -1)
	if c.PCIAddress != "" {
		for _, address := range addresses {
			if strings.EqualFold(address, c.PCIAddress) || strings.EqualFold("0000:"+address, c.PCIAddress) {
				return true
			}
		}
		return false
	}
	// Without a verified PCI mapping, generic driver messages cannot safely
	// be assigned to one of multiple SSA controllers.
	count := 0
	for _, candidate := range inv.Controllers {
		if candidate.Backend == c.Backend {
			count++
		}
	}
	if count != 1 {
		return false
	}
	return len(addresses) == 0 && (strings.HasPrefix(lower, "hpsa ") || strings.HasPrefix(lower, "hpsa:") || strings.HasPrefix(lower, "smartpqi ") || strings.HasPrefix(lower, "smartpqi:"))
}
func (r *Runner) kernelEvents(ctx context.Context, c Controller, inv vendorInventory) ([]eventRow, string, error) {
	if binary, e := r.deps.Processes.LookPath("journalctl"); e == nil {
		res := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Args: []string{"--dmesg", "--boot=0", "--no-pager", "--output=json", "--lines=all"}, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: maxDiagnosticBytes, MaxStderr: 1 << 20})
		if res.Err == nil && res.ExitCode == 0 && !res.TimedOut && !res.StdoutExceeded && strings.TrimSpace(string(res.Stderr)) == "" {
			rows, e := parseKernelJournal(res.Stdout, c, inv)
			if e == nil && len(strings.TrimSpace(string(res.Stdout))) > 0 {
				return rows, "journalctl", nil
			}
		}
	}
	binary, e := r.deps.Processes.LookPath("dmesg")
	if e != nil {
		return nil, "", fmt.Errorf("no readable kernel-event source: journalctl failed/unavailable and dmesg is unavailable")
	}
	res := r.deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Args: []string{"--color=never"}, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: maxDiagnosticBytes, MaxStderr: 1 << 20})
	if res.ExitCode != 0 && ctx.Err() == nil {
		message := strings.ToLower(string(res.Stderr))
		if strings.Contains(message, "unrecognized option") || strings.Contains(message, "invalid option") {
			res = r.deps.Processes.Run(ctx, core.ProcessSpec{Path: binary, Env: []string{"LC_ALL=C", "LANG=C"}, MaxStdout: maxDiagnosticBytes, MaxStderr: 1 << 20})
		}
	}
	if res.Err != nil || res.ExitCode != 0 || res.TimedOut || res.StdoutExceeded {
		return nil, "", fmt.Errorf("kernel-event source dmesg failed: %s", processMessage(res))
	}
	rows := []eventRow{}
	for _, raw := range strings.Split(string(res.Stdout), "\n") {
		line := strings.TrimSpace(raw)
		message := line
		stamp := ""
		if strings.HasPrefix(line, "[") {
			if end := strings.Index(line, "]"); end > 0 {
				stamp = "boot+" + strings.TrimSpace(line[1:end]) + "s"
				message = strings.TrimSpace(line[end+1:])
			}
		}
		if kernelMatches(message, c, inv) {
			rows = append(rows, eventRow{Controller: c.ID, Backend: c.Backend, Source: "kernel-dmesg", Time: stamp, Message: message})
		}
	}
	// dmesg reports records from oldest to newest. Preserve monotonic order
	// without pretending boot-relative timestamps are wall-clock dates.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, "dmesg", nil
}
func parseKernelJournal(data []byte, c Controller, inv vendorInventory) ([]eventRow, error) {
	rows := []eventRow{}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	for {
		var obj map[string]json.RawMessage
		e := dec.Decode(&obj)
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		var message string
		if json.Unmarshal(obj["MESSAGE"], &message) != nil {
			var bytes []byte
			if json.Unmarshal(obj["MESSAGE"], &bytes) != nil {
				continue
			}
			message = string(bytes)
		}
		var device string
		_ = json.Unmarshal(obj["_KERNEL_DEVICE"], &device)
		if !kernelMatches(message+" "+device, c, inv) {
			continue
		}
		row := eventRow{Controller: c.ID, Backend: c.Backend, Source: "kernel-journal", Message: message}
		var stamp, priority string
		_ = json.Unmarshal(obj["__REALTIME_TIMESTAMP"], &stamp)
		if stamp == "" {
			stamp = strings.TrimSpace(string(obj["__REALTIME_TIMESTAMP"]))
		}
		_ = json.Unmarshal(obj["PRIORITY"], &priority)
		if priority == "" {
			priority = strings.TrimSpace(string(obj["PRIORITY"]))
		}
		if us, e := strconv.ParseInt(stamp, 10, 64); e == nil && us > 0 {
			row.Time = time.UnixMicro(us).UTC().Format(time.RFC3339Nano)
		}
		if n, e := strconv.Atoi(priority); e == nil && n >= 0 && n <= 7 {
			row.Severity = []string{"emerg", "alert", "critical", "error", "warning", "notice", "info", "debug"}[n]
		}
		rows = append(rows, row)
	}
	return rows, nil
}
