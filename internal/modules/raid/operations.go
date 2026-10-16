package raid

import "toolctl/api/v1alpha1"

const (
	capabilityLocate = "raid.disk.locate"
	capabilityTasks  = "raid.task.list"
	capabilityHealth = "raid.health"
	capabilityEvents = "raid.event.list"
)

func stringFlag(name, desc string) v1alpha1.OptionSpec {
	return v1alpha1.OptionSpec{Name: name, Type: v1alpha1.OptionString, Description: desc}
}
func columnsFor(fields ...string) []v1alpha1.ColumnHint {
	out := []v1alpha1.ColumnHint{}
	for i := 0; i < len(fields); i += 2 {
		out = append(out, v1alpha1.ColumnHint{Header: fields[i], Path: "data." + fields[i+1], Type: v1alpha1.ColumnString, Order: i})
	}
	return out
}
func operationCapabilities() []v1alpha1.Capability {
	return []v1alpha1.Capability{
		{ID: capabilityLocate, Domain: "raid", Resource: "disk", Verb: "locate", Mutating: true, Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "locate"}}, Summary: "Locate exactly one physical disk for 10 minutes, or stop its locator", Options: []v1alpha1.OptionSpec{
			stringFlag("controller", "Exact vendor controller ID (required)"), stringFlag("enclosure", "Exact vendor enclosure ID"), stringFlag("slot", "Exact vendor slot"), stringFlag("device", "Vendor physical-device address, e.g. 0:1 or 1I:1:2"),
			{Name: "stop", Type: v1alpha1.OptionBool, Description: "Turn off the locator immediately"},
			{Name: "duration", Type: v1alpha1.OptionDuration, Default: []byte("600000"), Description: "Locator duration (foreground; 1s to 24h)"},
		}, Columns: columnsFor("HOST", "host", "CONTROLLER", "controller", "ENCLOSURE", "enclosure", "SLOT", "slot", "DEVICE", "id", "STATUS", "status", "LOCATE", "locate")},
		{ID: capabilityTasks, Domain: "raid", Resource: "task", Verb: "list", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "tasks"}}, Summary: "Show background tasks and vendor-reported progress", Options: []v1alpha1.OptionSpec{
			{Name: "live", Shorthand: "l", Type: v1alpha1.OptionBool, Description: "Refresh until interrupted"},
			{Name: "interval", Shorthand: "i", Type: v1alpha1.OptionDuration, Default: []byte("5000"), Description: "Refresh interval"},
		}, Columns: columnsFor("CONTROLLER", "controller", "BACKEND", "backend", "TARGET", "target", "TASK", "task", "STATUS", "status", "PROGRESS", "progress", "DETAILS", "details")},
		{ID: capabilityHealth, Domain: "raid", Resource: "health", Verb: "get", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "health"}}, Summary: "Show disk health, cache policy and cache-protection evidence", Columns: columnsFor("CONTROLLER", "controller", "BACKEND", "backend", "TARGET", "target", "CHECK", "check", "STATUS", "status", "VALUE", "value")},
		{ID: capabilityEvents, Domain: "raid", Resource: "event", Verb: "list", Command: v1alpha1.CommandPathSpec{Path: []string{"raid", "events"}}, Summary: "Read recent controller events without clearing logs", Options: []v1alpha1.OptionSpec{
			{Name: "count", Shorthand: "c", Type: v1alpha1.OptionInt, Default: []byte("20"), Description: "Maximum recent entries per controller (1 to 1000)"},
		}, Columns: columnsFor("CONTROLLER", "controller", "BACKEND", "backend", "SOURCE", "source", "TIME", "time", "SEVERITY", "severity", "EVENT", "message")},
	}
}
