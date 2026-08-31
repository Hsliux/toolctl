package registry

import (
	"fmt"
	"sort"
	"strings"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type Registration struct {
	Module     v1alpha1.ModuleInfo
	Capability v1alpha1.Capability
	Runner     core.Runner
}

type Registry struct {
	byID   map[string]Registration
	byPath map[string]string
	caps   []v1alpha1.Capability
}

func New(modules []core.Module, deps core.Dependencies) (*Registry, error) {
	r := &Registry{byID: map[string]Registration{}, byPath: map[string]string{}}
	for _, module := range modules {
		info := module.Info()
		runner, err := module.NewRunner(deps)
		if err != nil {
			return nil, fmt.Errorf("initialize module %s: %w", info.Name, err)
		}
		for _, capability := range module.Capabilities() {
			if err := validateCapability(capability); err != nil {
				return nil, fmt.Errorf("module %s: %w", info.Name, err)
			}
			if previous, ok := r.byID[capability.ID]; ok {
				return nil, fmt.Errorf("capability %s is registered by both %s and %s", capability.ID, previous.Module.Name, info.Name)
			}
			registration := Registration{Module: info, Capability: capability, Runner: runner}
			r.byID[capability.ID] = registration
			paths := append([][]string{capability.Command.Path}, capability.Command.Aliases...)
			for _, path := range paths {
				key := pathKey(path)
				if previousID, ok := r.byPath[key]; ok {
					return nil, fmt.Errorf("command path %q conflicts between %s and %s", strings.Join(path, " "), previousID, capability.ID)
				}
				r.byPath[key] = capability.ID
			}
			r.caps = append(r.caps, capability)
		}
	}
	sort.Slice(r.caps, func(i, j int) bool {
		return strings.Join(r.caps[i].Command.Path, " ") < strings.Join(r.caps[j].Command.Path, " ")
	})
	return r, nil
}

func (r *Registry) Resolve(id string) (Registration, bool) {
	registration, ok := r.byID[id]
	return registration, ok
}

func (r *Registry) Capabilities() []v1alpha1.Capability {
	return append([]v1alpha1.Capability(nil), r.caps...)
}

func validateCapability(capability v1alpha1.Capability) error {
	if capability.ID == "" {
		return fmt.Errorf("capability ID is empty")
	}
	if len(capability.Command.Path) == 0 {
		return fmt.Errorf("capability %s has an empty command path", capability.ID)
	}
	if capability.PassthroughArgs && capability.NameMode != "" && capability.NameMode != v1alpha1.NameNone {
		return fmt.Errorf("capability %s cannot combine passthrough arguments with a resource name", capability.ID)
	}
	for _, path := range append([][]string{capability.Command.Path}, capability.Command.Aliases...) {
		for _, token := range path {
			if token == "" || strings.ToLower(token) != token || strings.ContainsAny(token, " _./") {
				return fmt.Errorf("capability %s has invalid command token %q", capability.ID, token)
			}
		}
	}
	reserved := map[string]struct{}{"help": {}, "output": {}, "no-headers": {}, "timeout": {}, "context": {}, "target": {}, "selector": {}, "concurrency": {}, "verbose": {}}
	seenOptions := map[string]struct{}{}
	seenShorthands := map[string]struct{}{}
	reservedShorthands := map[string]struct{}{"h": {}, "o": {}}
	for _, option := range capability.Options {
		if option.Name == "" || strings.ToLower(option.Name) != option.Name || strings.ContainsAny(option.Name, " _./") {
			return fmt.Errorf("capability %s has invalid option name %q", capability.ID, option.Name)
		}
		if _, ok := reserved[option.Name]; ok {
			return fmt.Errorf("capability %s option %q conflicts with a global flag", capability.ID, option.Name)
		}
		if _, ok := seenOptions[option.Name]; ok {
			return fmt.Errorf("capability %s declares option %q more than once", capability.ID, option.Name)
		}
		seenOptions[option.Name] = struct{}{}
		if option.Shorthand != "" {
			characters := []rune(option.Shorthand)
			if len(characters) != 1 || !(characters[0] >= 'a' && characters[0] <= 'z' || characters[0] >= 'A' && characters[0] <= 'Z') {
				return fmt.Errorf("capability %s option %q has invalid shorthand %q", capability.ID, option.Name, option.Shorthand)
			}
			if _, ok := seenShorthands[option.Shorthand]; ok {
				return fmt.Errorf("capability %s declares shorthand %q more than once", capability.ID, option.Shorthand)
			}
			if _, ok := reservedShorthands[option.Shorthand]; ok {
				return fmt.Errorf("capability %s option %q shorthand %q conflicts with a global flag", capability.ID, option.Name, option.Shorthand)
			}
			seenShorthands[option.Shorthand] = struct{}{}
		}
		switch option.Type {
		case v1alpha1.OptionString, v1alpha1.OptionBool, v1alpha1.OptionInt, v1alpha1.OptionDuration, v1alpha1.OptionStringSlice:
		default:
			return fmt.Errorf("capability %s option %q has unsupported type %q", capability.ID, option.Name, option.Type)
		}
	}
	seenHeaders := map[string]struct{}{}
	for _, column := range capability.Columns {
		if column.Header == "" || column.Path == "" {
			return fmt.Errorf("capability %s has a column with an empty header or path", capability.ID)
		}
		if _, ok := seenHeaders[column.Header]; ok {
			return fmt.Errorf("capability %s declares column %q more than once", capability.ID, column.Header)
		}
		seenHeaders[column.Header] = struct{}{}
		switch column.Type {
		case v1alpha1.ColumnString, v1alpha1.ColumnInteger, v1alpha1.ColumnBoolean, v1alpha1.ColumnDecimal, v1alpha1.ColumnPercent, v1alpha1.ColumnBytes, v1alpha1.ColumnBytesPerSecond,
			v1alpha1.ColumnDurationSeconds, v1alpha1.ColumnCount, v1alpha1.ColumnNamedBytesList:
		default:
			return fmt.Errorf("capability %s column %q has unsupported type %q", capability.ID, column.Header, column.Type)
		}
	}
	return nil
}

func pathKey(path []string) string { return strings.Join(path, "\x00") }
