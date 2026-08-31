package raid

import (
	"debug/elf"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"toolctl/internal/core"
)

type toolDefinition struct {
	Backend string
	Names   []string
	Paths   []string
}

var toolDefinitions = []toolDefinition{
	{Backend: "perccli", Names: []string{"perccli64", "perccli"}, Paths: []string{"/opt/MegaRAID/perccli/perccli64", "/opt/MegaRAID/perccli/perccli"}},
	{Backend: "storcli", Names: []string{"storcli64", "storcli2", "storcli"}, Paths: []string{"/opt/MegaRAID/storcli/storcli64", "/opt/MegaRAID/storcli/storcli", "/opt/MegaRAID/storcli2/storcli2"}},
	{Backend: "ssacli", Names: []string{"ssacli", "hpssacli"}, Paths: []string{"/usr/sbin/ssacli", "/usr/sbin/hpssacli"}},
	{Backend: "arcconf", Names: []string{"arcconf"}, Paths: []string{"/usr/Arcconf/arcconf", "/usr/sbin/arcconf"}},
}

type toolConfig struct {
	APIVersion string             `json:"apiVersion"`
	Tools      []ToolRegistration `json:"tools"`
}

type toolConfigLocation struct {
	Directory string
	Scope     string
}

type toolSource struct {
	Path       string
	Scope      string
	ConfigPath string
}

func definition(backend string) (toolDefinition, bool) {
	backend = strings.ToLower(strings.TrimSpace(backend))
	for _, item := range toolDefinitions {
		if item.Backend == backend {
			return item, true
		}
	}
	return toolDefinition{}, false
}

func configPath(directory string) string {
	if directory == "" {
		return ""
	}
	return filepath.Join(directory, "raid-tools.json")
}

func loadTools(directories ...string) map[string]string {
	locations := make([]toolConfigLocation, 0, len(directories))
	for _, directory := range directories {
		locations = append(locations, toolConfigLocation{Directory: directory})
	}
	return toolSourcePaths(loadToolSources(locations...))
}

func loadConfiguredToolSources(deps core.Dependencies) map[string]toolSource {
	scope := deps.ConfigScope
	if scope == "" {
		scope = "user"
	}
	return loadToolSources(
		toolConfigLocation{Directory: deps.SystemConfigDirectory, Scope: "system"},
		toolConfigLocation{Directory: deps.ConfigDirectory, Scope: scope},
	)
}

func loadToolSources(locations ...toolConfigLocation) map[string]toolSource {
	result := map[string]toolSource{}
	for _, location := range locations {
		path := configPath(location.Directory)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var config toolConfig
		if json.Unmarshal(data, &config) != nil {
			continue
		}
		for _, item := range config.Tools {
			if _, ok := definition(item.Backend); ok && filepath.IsAbs(item.Path) {
				result[item.Backend] = toolSource{Path: item.Path, Scope: location.Scope, ConfigPath: path}
			}
		}
	}
	return result
}

func toolSourcePaths(sources map[string]toolSource) map[string]string {
	result := make(map[string]string, len(sources))
	for backend, source := range sources {
		result[backend] = source.Path
	}
	return result
}

func saveTools(directory string, tools map[string]string) error {
	if directory == "" {
		return fmt.Errorf("configuration directory is unavailable")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	backends := make([]string, 0, len(tools))
	for backend := range tools {
		backends = append(backends, backend)
	}
	sort.Strings(backends)
	config := toolConfig{APIVersion: "toolctl.io/v1alpha1", Tools: []ToolRegistration{}}
	for _, backend := range backends {
		config.Tools = append(config.Tools, ToolRegistration{Backend: backend, Path: tools[backend]})
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(directory, ".raid-tools-*")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, configPath(directory)); err != nil {
		return fmt.Errorf("replace RAID tool configuration: %w", err)
	}
	return nil
}

func discoverTool(processes core.ProcessExecutor, backend string, registered map[string]string) (path string, isRegistered bool) {
	if configured := registered[backend]; configured != "" && executable(configured) {
		return configured, true
	}
	definition, ok := definition(backend)
	if !ok {
		return "", false
	}
	for _, candidate := range definition.Paths {
		if executable(candidate) {
			return candidate, false
		}
	}
	for _, name := range definition.Names {
		if candidate, err := processes.LookPath(name); err == nil && executable(candidate) {
			return candidate, false
		}
	}
	return "", false
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

func architectureCompatible(path, architecture string) bool {
	file, err := elf.Open(path)
	if err != nil {
		// Wrapper scripts and non-ELF launchers are allowed; their interpreter decides compatibility.
		return true
	}
	defer file.Close()
	switch architecture {
	case "amd64":
		return file.Machine == elf.EM_X86_64
	case "arm64":
		return file.Machine == elf.EM_AARCH64
	default:
		return false
	}
}
