package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"toolctl/api/v1alpha1"
	"toolctl/internal/apperror"
	"toolctl/internal/core"
	"toolctl/internal/render"
)

type BuildInfo struct {
	Version   string `json:"version" yaml:"version"`
	Commit    string `json:"commit" yaml:"commit"`
	BuildTime string `json:"buildTime" yaml:"buildTime"`
	GoVersion string `json:"goVersion" yaml:"goVersion"`
}

type ExecuteFunc func(context.Context, v1alpha1.Operation, render.Options) (int, error)

const (
	groupServer     = "server"
	groupDiagnostic = "diagnostic"
	groupStorage    = "storage"
	groupExecution  = "execution"
	groupDatabase   = "database"
	groupBenchmark  = "benchmark"
	groupOther      = "other"
)

var rootCommandGroups = []*cobra.Group{
	{ID: groupServer, Title: "Server Information Commands:"},
	{ID: groupDiagnostic, Title: "Troubleshooting and Performance Commands:"},
	{ID: groupStorage, Title: "Storage and RAID Commands:"},
	{ID: groupExecution, Title: "Container and Batch Commands:"},
	{ID: groupDatabase, Title: "Database Commands:"},
	{ID: groupBenchmark, Title: "Benchmark and Assessment Commands:"},
	{ID: groupOther, Title: "Configuration and Other Commands:"},
}

var commandSummaries = map[string]string{
	"assess":          "Run a quick or full new-server assessment",
	"batch":           "Execute commands across SSH hosts or local containers",
	"bench":           "Benchmark CPU, memory, disk, devices, and network",
	"disk":            "Inspect disk health",
	"init":            "Initialize local tool configuration",
	"mysql":           "Show key MySQL or MariaDB operational metrics",
	"net":             "Diagnose DNS, routing, and TCP connectivity",
	"perf":            "Inspect live and historical system performance",
	"perf\x00history": "Query performance history through the optional ssar backend",
	"perf\x00tcp":     "Inspect TCP performance counters",
	"raid":            "Inspect Linux MD and supported hardware RAID",
	"redis":           "Show key Redis operational metrics",
}

type CLI struct {
	root      *cobra.Command
	exitCode  int
	output    string
	noHeaders bool
	timeout   time.Duration
	ids       core.IDGenerator
	execute   ExecuteFunc
	stdout    io.Writer
	stderr    io.Writer
}

type reportedError struct{ cause error }

func (e *reportedError) Error() string { return e.cause.Error() }
func (e *reportedError) Unwrap() error { return e.cause }

// ErrorReported reports whether the CLI already printed the error together
// with contextual help. Callers should not print the same error again.
func ErrorReported(err error) bool {
	var reported *reportedError
	return errors.As(err, &reported)
}

func New(info BuildInfo, capabilities []v1alpha1.Capability, ids core.IDGenerator, execute ExecuteFunc, stdout, stderr io.Writer) (*CLI, error) {
	cli := &CLI{ids: ids, execute: execute, stdout: stdout, stderr: stderr, exitCode: 0}
	root := &cobra.Command{
		Use:   "toolctl",
		Short: "Inspect, diagnose, and benchmark Linux servers",
		Long: `toolctl is a unified Linux server inspection and operations CLI.

It provides dependency-free host inspection, live performance sampling and
network diagnostics, with optional integrations for databases, RAID, fio and ssar.`,
		Example: `  # Inspect a newly received server
  toolctl doctor -o wide
  toolctl host -o wide
  toolctl health

  # Observe live performance
  toolctl perf cpu -l

  # Run the default quick server assessment
  toolctl assess`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddGroup(rootCommandGroups...)
	root.PersistentFlags().StringVarP(&cli.output, "output", "o", "table", "Output format: table, wide, json, or yaml")
	root.PersistentFlags().BoolVar(&cli.noHeaders, "no-headers", false, "Do not print table headers")
	root.PersistentFlags().DurationVar(&cli.timeout, "timeout", 30*time.Second, "Operation timeout")
	version := cli.versionCommand(info)
	version.GroupID = groupOther
	root.AddCommand(version)
	cli.root = root
	commands := map[string]*cobra.Command{"": root}
	for _, capability := range capabilities {
		paths := append([][]string{capability.Command.Path}, capability.Command.Aliases...)
		for index, path := range paths {
			if err := cli.addPath(commands, capability, path, index > 0); err != nil {
				return nil, err
			}
		}
	}
	// Initialize Cobra's built-in commands here so they participate in the
	// grouped root help instead of appearing in a separate catch-all section.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	for _, command := range root.Commands() {
		if command.Name() == "help" || command.Name() == "completion" {
			command.GroupID = groupOther
		}
	}
	enableParentHelpFallback(root)
	return cli, nil
}

func enableParentHelpFallback(command *cobra.Command) {
	for _, child := range command.Commands() {
		enableParentHelpFallback(child)
	}
	if command.Parent() == nil || command.Runnable() || !command.HasSubCommands() {
		return
	}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
		}
		return cmd.Help()
	}
}

func (c *CLI) Execute(ctx context.Context) (int, error) {
	command, err := c.root.ExecuteContextC(ctx)
	if err != nil && !apperror.Is(err) {
		err = apperror.Wrap(v1alpha1.ErrorInvalidArgument, err.Error(), err)
	}
	if err != nil && apperror.Code(err) == v1alpha1.ErrorInvalidArgument {
		if command == nil {
			command = c.root
		}
		_, _ = fmt.Fprintf(c.stderr, "error: %s\n\n", err)
		command.SetOut(c.stderr)
		_ = command.Help()
		command.SetOut(c.stdout)
		return c.exitCode, &reportedError{cause: err}
	}
	return c.exitCode, err
}

func (c *CLI) SetArgs(args []string) { c.root.SetArgs(args) }

func (c *CLI) addPath(commands map[string]*cobra.Command, capability v1alpha1.Capability, path []string, alias bool) error {
	parent := c.root
	for index, token := range path {
		key := strings.Join(path[:index+1], "\x00")
		command, exists := commands[key]
		last := index == len(path)-1
		if !exists {
			command = &cobra.Command{Use: token, Short: commandSummary(path[:index+1])}
			if parent == c.root {
				command.GroupID = rootCommandGroup(token)
			}
			commands[key] = command
			parent.AddCommand(command)
		}
		if last {
			if command.RunE != nil {
				return fmt.Errorf("command path %q is already executable", strings.Join(path, " "))
			}
			if err := configureLeaf(command, capability, c, alias); err != nil {
				return err
			}
		}
		parent = command
	}
	return nil
}

func commandSummary(path []string) string {
	if summary, ok := commandSummaries[strings.Join(path, "\x00")]; ok {
		return summary
	}
	return "Commands for " + strings.ReplaceAll(path[len(path)-1], "-", " ")
}

func rootCommandGroup(command string) string {
	switch command {
	case "host", "disks", "nics":
		return groupServer
	case "doctor", "health", "top", "perf", "net", "disk":
		return groupDiagnostic
	case "raid":
		return groupStorage
	case "containers", "batch":
		return groupExecution
	case "mysql", "redis":
		return groupDatabase
	case "bench", "burn", "assess":
		return groupBenchmark
	case "init", "version", "completion", "help":
		return groupOther
	default:
		// Capabilities can be added without changing the CLI builder. Keep new
		// top-level commands discoverable until they receive a dedicated group.
		return groupOther
	}
}

type optionBinding struct {
	spec   v1alpha1.OptionSpec
	encode func() (json.RawMessage, error)
}

func configureLeaf(command *cobra.Command, capability v1alpha1.Capability, cli *CLI, alias bool) error {
	command.Short = capability.Summary
	command.Hidden = alias
	if capability.PassthroughArgs {
		command.Args = cobra.MinimumNArgs(1)
		command.Use += " -- <command> [args...]"
	} else {
		switch capability.NameMode {
		case v1alpha1.NameRequired:
			command.Args = cobra.ExactArgs(1)
			command.Use += " <name>"
		case v1alpha1.NameOptional:
			command.Args = cobra.MaximumNArgs(1)
			command.Use += " [name]"
		default:
			command.Args = cobra.NoArgs
		}
	}
	bindings, err := bindOptions(command, capability.Options)
	if err != nil {
		return fmt.Errorf("capability %s: %w", capability.ID, err)
	}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		format, err := render.ParseFormat(cli.output)
		if err != nil {
			return apperror.Wrap(v1alpha1.ErrorInvalidArgument, err.Error(), err)
		}
		if cli.timeout <= 0 {
			return apperror.New(v1alpha1.ErrorInvalidArgument, "timeout must be greater than zero")
		}
		name := ""
		if !capability.PassthroughArgs && len(args) == 1 {
			name = args[0]
		}
		options := map[string]json.RawMessage{}
		for _, binding := range bindings {
			changed := cmd.Flags().Changed(binding.spec.Name)
			if !changed && len(binding.spec.Default) == 0 {
				if binding.spec.Required {
					return apperror.New(v1alpha1.ErrorInvalidArgument, "required option --"+binding.spec.Name+" is missing")
				}
				continue
			}
			value, err := binding.encode()
			if err != nil {
				return apperror.Wrap(v1alpha1.ErrorInvalidArgument, "invalid option --"+binding.spec.Name, err)
			}
			options[binding.spec.Name] = value
		}
		op := v1alpha1.Operation{
			APIVersion: v1alpha1.APIVersion,
			ID:         cli.ids.NewID(),
			Capability: capability.ID,
			Name:       name,
			Targets:    []v1alpha1.Target{},
			Arguments:  []string{},
			Options:    options,
			TimeoutMS:  cli.timeout.Milliseconds(),
		}
		if capability.PassthroughArgs {
			op.Arguments = append([]string(nil), args...)
		}
		// A live operation is intentionally unbounded and ends on cancellation.
		// The sampling interval remains bounded by the module's own validation.
		if raw, ok := options["live"]; ok {
			var live bool
			if json.Unmarshal(raw, &live) == nil && live {
				op.TimeoutMS = 0
			}
		}
		columns := capability.Columns
		var service bool
		_ = json.Unmarshal(options["service"], &service)
		if service && (capability.ID == "raid.overview" || capability.ID == "raid.disk.list") {
			if format != render.FormatTable && format != render.FormatWide {
				return apperror.New(v1alpha1.ErrorInvalidArgument, "--service supports table and wide output")
			}
			columns = []v1alpha1.ColumnHint{}
			for i, field := range []string{"host", "controller", "enclosure", "slot", "status", "locate", "handoff"} {
				columns = append(columns, v1alpha1.ColumnHint{Header: strings.ToUpper(field), Path: "data." + field, Type: v1alpha1.ColumnString, Order: i})
			}
		}
		exitCode, err := cli.execute(cmd.Context(), op, render.Options{Format: format, NoHeaders: cli.noHeaders, Columns: columns})
		if err == nil {
			cli.exitCode = exitCode
		}
		return err
	}
	return nil
}

func bindOptions(command *cobra.Command, specs []v1alpha1.OptionSpec) ([]optionBinding, error) {
	bindings := make([]optionBinding, 0, len(specs))
	for _, spec := range specs {
		switch spec.Type {
		case v1alpha1.OptionString:
			defaultValue := ""
			if err := decodeDefault(spec.Default, &defaultValue); err != nil {
				return nil, fmt.Errorf("default for --%s: %w", spec.Name, err)
			}
			value := command.Flags().StringP(spec.Name, spec.Shorthand, defaultValue, spec.Description)
			bindings = append(bindings, optionBinding{spec: spec, encode: func() (json.RawMessage, error) { return json.Marshal(*value) }})
		case v1alpha1.OptionBool:
			defaultValue := false
			if err := decodeDefault(spec.Default, &defaultValue); err != nil {
				return nil, fmt.Errorf("default for --%s: %w", spec.Name, err)
			}
			value := command.Flags().BoolP(spec.Name, spec.Shorthand, defaultValue, spec.Description)
			bindings = append(bindings, optionBinding{spec: spec, encode: func() (json.RawMessage, error) { return json.Marshal(*value) }})
		case v1alpha1.OptionInt:
			defaultValue := int64(0)
			if err := decodeDefault(spec.Default, &defaultValue); err != nil {
				return nil, fmt.Errorf("default for --%s: %w", spec.Name, err)
			}
			value := command.Flags().Int64P(spec.Name, spec.Shorthand, defaultValue, spec.Description)
			bindings = append(bindings, optionBinding{spec: spec, encode: func() (json.RawMessage, error) { return json.Marshal(*value) }})
		case v1alpha1.OptionDuration:
			defaultMS := int64(0)
			if err := decodeDefault(spec.Default, &defaultMS); err != nil {
				return nil, fmt.Errorf("default for --%s: %w", spec.Name, err)
			}
			value := command.Flags().DurationP(spec.Name, spec.Shorthand, time.Duration(defaultMS)*time.Millisecond, spec.Description)
			bindings = append(bindings, optionBinding{spec: spec, encode: func() (json.RawMessage, error) { return json.Marshal(value.Milliseconds()) }})
		case v1alpha1.OptionStringSlice:
			defaultValue := []string{}
			if err := decodeDefault(spec.Default, &defaultValue); err != nil {
				return nil, fmt.Errorf("default for --%s: %w", spec.Name, err)
			}
			value := command.Flags().StringSliceP(spec.Name, spec.Shorthand, defaultValue, spec.Description)
			bindings = append(bindings, optionBinding{spec: spec, encode: func() (json.RawMessage, error) { return json.Marshal(*value) }})
		default:
			return nil, fmt.Errorf("unsupported option type %q for --%s", spec.Type, spec.Name)
		}
	}
	return bindings, nil
}

func decodeDefault(raw json.RawMessage, destination any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, destination)
}

func (c *CLI) versionCommand(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show toolctl version information",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			switch strings.ToLower(c.output) {
			case "json":
				encoder := json.NewEncoder(c.stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(info)
			case "yaml":
				data, err := yaml.Marshal(info)
				if err != nil {
					return err
				}
				_, err = c.stdout.Write(data)
				return err
			case "table", "wide":
				_, err := fmt.Fprintf(c.stdout, "toolctl %s (%s, %s, %s)\n", info.Version, info.Commit, info.BuildTime, info.GoVersion)
				return err
			default:
				return apperror.New(v1alpha1.ErrorInvalidArgument, "unsupported output format: "+c.output)
			}
		},
	}
}
