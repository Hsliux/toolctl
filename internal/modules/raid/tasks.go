package raid

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/apperror"
	"toolctl/internal/core"
	resultbuilder "toolctl/internal/result"
)

type Task struct {
	Controller string `json:"controller"`
	Backend    string `json:"backend"`
	Target     string `json:"target"`
	Task       string `json:"task"`
	Status     string `json:"status"`
	Progress   string `json:"progress,omitempty"`
	Details    string `json:"details,omitempty"`
}

var percentPattern = regexp.MustCompile(`(?:^|[^0-9.+-])([0-9]+(?:\.[0-9]+)?)\s*%`)

func progressPercent(value string) string {
	if m := percentPattern.FindStringSubmatch(value); len(m) == 2 {
		n, e := strconv.ParseFloat(m[1], 64)
		if e == nil && n >= 0 && n <= 100 {
			return m[1] + "%"
		}
	}
	return ""
}
func (r *Runner) tasks(ctx context.Context, op v1alpha1.Operation) (core.RunOutput, error) {
	ms, e := durationOption(op, "interval", 5000)
	if e != nil {
		return core.RunOutput{}, e
	}
	if ms < 1000 || ms > 3600000 {
		return core.RunOutput{}, apperror.New(v1alpha1.ErrorInvalidArgument, "interval must be between 1s and 1h")
	}
	s, e := parseSelection(op)
	if e != nil {
		return core.RunOutput{}, e
	}
	inv, warnings := r.inventory(ctx)
	out := core.RunOutput{Items: []v1alpha1.Item{}, Warnings: warnings, Errors: []v1alpha1.TargetError{}}
	configured := toolSourcePaths(loadConfiguredToolSources(r.deps))
	seen := map[string]bool{}
	appendRows := func(rows []Task) error {
		for i, t := range rows {
			item, e := resultbuilder.NewItem("RAIDTask", t.Backend+"/"+t.Controller+"/"+t.Task+"/"+t.Target+fmt.Sprint(i), "", t)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, item)
		}
		return nil
	}
	for _, c := range inv.Controllers {
		if (s.backend != "" && s.backend != c.Backend) || (s.controller != "" && s.controller != c.ID) {
			continue
		}
		key := c.Backend + "/" + c.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		binary, _ := discoverTool(r.deps.Processes, c.Backend, configured)
		if !numericDiskAddress.MatchString(c.ID) {
			continue
		}
		queries := []struct {
			name string
			args []string
		}{}
		switch c.Backend {
		case "arcconf":
			queries = append(queries, struct {
				name string
				args []string
			}{"background", []string{"GETSTATUS", c.ID, "nologs"}})
		case "ssacli":
			queries = append(queries, struct {
				name string
				args []string
			}{"background", []string{"ctrl", "slot=" + c.ID, "show", "config", "detail"}})
		case "storcli", "perccli":
			for _, q := range []struct{ name, path, verb string }{{"rebuild", "/eall/sall", "rebuild"}, {"initialization", "/vall", "init"}, {"consistency-check", "/vall", "cc"}, {"background-initialization", "/vall", "bgi"}} {
				queries = append(queries, struct {
					name string
					args []string
				}{q.name, []string{"/c" + c.ID + q.path, "show", q.verb, "J"}})
			}
		case "megacli":
			for _, d := range inv.Disks {
				if d.Backend == c.Backend && d.Controller == c.ID && physicalDiskStatus(d) == "rebuilding" && validDiskAddress(d) {
					queries = append(queries, struct {
						name string
						args []string
					}{"rebuild " + physicalDiskLocation(d), []string{"-PDRbld", "-ShowProg", "-PhysDrv[" + d.Enclosure + ":" + d.Slot + "]", "-a" + c.ID, "-NoLog"}})
				}
			}
			for _, q := range []struct{ name, verb string }{{"initialization", "-LDInit"}, {"consistency-check", "-LDCC"}, {"background-initialization", "-LDBI"}} {
				queries = append(queries, struct {
					name string
					args []string
				}{q.name, []string{q.verb, "-ShowProg", "-LALL", "-a" + c.ID, "-NoLog"}})
			}
		default:
			out.Warnings = append(out.Warnings, diagnostic("RAID_TASKS_UNSUPPORTED", "task query unsupported for "+c.Backend, nil))
		}
		for _, q := range queries {
			data, e := r.memberQuery(ctx, binary, q.args)
			if e != nil {
				out.Errors = append(out.Errors, v1alpha1.TargetError{Code: v1alpha1.ErrorExecutionFailed, Message: c.Backend + " " + q.name + ": " + e.Error()})
				continue
			}
			var tasks []Task
			if c.Backend == "storcli" || c.Backend == "perccli" {
				tasks, e = parseJSONTasks(data, c.ID, c.Backend, q.name)
			} else {
				tasks, e = parseTextTasks(data, c.ID, c.Backend, q.name)
			}
			if e != nil {
				out.Errors = append(out.Errors, v1alpha1.TargetError{Code: v1alpha1.ErrorExecutionFailed, Message: e.Error()})
				continue
			}
			if strings.HasPrefix(q.name, "rebuild ") {
				for i := range tasks {
					tasks[i].Target = strings.TrimPrefix(q.name, "rebuild ")
					tasks[i].Task = "rebuild"
				}
			}
			if e := appendRows(tasks); e != nil {
				return out, e
			}
		}
	}
	for _, v := range inv.Volumes {
		if v.Backend != "mdraid" || (s.backend != "" && s.backend != "mdraid") || (s.controller != "" && s.controller != "mdraid") {
			continue
		}
		action, e := readValue(r.deps.Files, "/sys/block/"+v.ID+"/md/sync_action")
		if e != nil {
			out.Warnings = append(out.Warnings, diagnostic("RAID_TASK_PROGRESS_UNAVAILABLE", v.ID+": "+e.Error(), nil))
			continue
		}
		t := Task{Controller: "mdraid", Backend: "mdraid", Target: v.ID, Task: action, Status: "running"}
		if action == "idle" || action == "frozen" {
			t.Status = action
		}
		if done, e := readValue(r.deps.Files, "/sys/block/"+v.ID+"/md/sync_completed"); e == nil {
			parts := strings.Split(done, "/")
			if len(parts) == 2 {
				a, ae := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
				b, be := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
				if ae == nil && be == nil && b > 0 {
					t.Progress = fmt.Sprintf("%.1f%%", a/b*100)
				}
			}
		}
		if e := appendRows([]Task{t}); e != nil {
			return out, e
		}
	}
	completeQuery(&out, inv, s)
	return out, nil
}

func parseTextTasks(data []byte, controller, backend, name string) ([]Task, error) {
	text := strings.TrimSpace(string(data))
	lower := strings.ToLower(text)
	idleReported := strings.Contains(lower, "not in progress") || strings.Contains(lower, "no tasks") || strings.Contains(lower, "no operation")
	rows := []Task{}
	target := "-"
	task := name
	pending := -1
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		key, value, ok := splitVendorField(line)
		lkey := strings.ToLower(key)
		if m := megaVirtualDrive.FindStringSubmatch(line); len(m) == 2 {
			target = "volume " + m[1]
			pending = -1
		}
		if m := ssaPhysicalHeader.FindStringSubmatch(line); len(m) == 2 {
			target = m[1]
			pending = -1
		}
		if m := ssaLogicalCompact.FindStringSubmatch(line); len(m) == 5 {
			target = "volume " + m[1]
			pending = -1
		}
		if ok && (lkey == "task type" || lkey == "task type for this logical device") {
			task = value
			pending = -1
		}
		if ok && (lkey == "logical device" || lkey == "logical device number" || lkey == "channel,device") {
			target = value
			if pending >= 0 {
				rows[pending].Target = target
			}
		}
		p := progressPercent(line)
		if backend == "ssacli" && !strings.Contains(lkey, "rebuild") && !strings.Contains(lkey, "initializ") && !strings.Contains(lkey, "consistency") && !strings.Contains(lkey, "transform") && !strings.Contains(lkey, "expand") && !strings.Contains(lkey, "migrat") && !strings.Contains(lkey, "progress") && !strings.Contains(lkey, "percentage") {
			p = ""
		}
		if p == "" && ok && (strings.Contains(lkey, "percentage complete") || strings.Contains(lkey, "progress percent")) {
			p = progressPercent(value + "%")
		}
		if p != "" {
			if pending >= 0 && rows[pending].Progress == "" {
				rows[pending].Progress = p
				rows[pending].Details += "; " + line
			} else {
				rows = append(rows, Task{Controller: controller, Backend: backend, Target: target, Task: task, Status: "running", Progress: p, Details: line})
				pending = len(rows) - 1
			}
		} else if strings.Contains(strings.ToLower(line), "rebuilding") || strings.EqualFold(lkey, "task type") {
			rows = append(rows, Task{Controller: controller, Backend: backend, Target: target, Task: task, Status: "running", Details: line})
			pending = len(rows) - 1
		}
	}
	if len(rows) > 0 {
		return rows, nil
	}
	if idleReported {
		return []Task{{Controller: controller, Backend: backend, Target: "-", Task: name, Status: "idle"}}, nil
	}
	if backend == "ssacli" && (strings.Contains(lower, "logical drive:") || strings.Contains(lower, "logicaldrive")) {
		return []Task{{Controller: controller, Backend: backend, Target: "-", Task: name, Status: "not-reported", Details: "No task/progress fields in SSA config; absence does not prove idle"}}, nil
	}
	return nil, fmt.Errorf("%s %s task output not recognized", backend, name)
}

func (r *Runner) streamTasks(ctx context.Context, op v1alpha1.Operation, emit func(core.RunOutput) error) error {
	ms, e := durationOption(op, "interval", 5000)
	if e != nil {
		return e
	}
	if ms < 1000 || ms > 3600000 {
		return apperror.New(v1alpha1.ErrorInvalidArgument, "interval must be between 1s and 1h")
	}
	for ctx.Err() == nil {
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		out, e := r.tasks(qctx, op)
		cancel()
		if e != nil {
			return e
		}
		if e := emit(out); e != nil {
			return e
		}
		if r.deps.Waiter != nil {
			if e := r.deps.Waiter.Wait(ctx, time.Duration(ms)*time.Millisecond); e != nil {
				return e
			}
		} else {
			timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}
