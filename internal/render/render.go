package render

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"sigs.k8s.io/yaml"

	"toolctl/api/v1alpha1"
)

type OutputFormat string

const (
	FormatTable OutputFormat = "table"
	FormatWide  OutputFormat = "wide"
	FormatJSON  OutputFormat = "json"
	FormatYAML  OutputFormat = "yaml"
)

type Options struct {
	Format    OutputFormat
	NoHeaders bool
	Columns   []v1alpha1.ColumnHint
}

func ParseFormat(value string) (OutputFormat, error) {
	format := OutputFormat(strings.ToLower(value))
	switch format {
	case FormatTable, FormatWide, FormatJSON, FormatYAML:
		return format, nil
	default:
		return "", fmt.Errorf("unsupported output format %q (use table, wide, json, or yaml)", value)
	}
}

func Write(w io.Writer, result v1alpha1.Result, options Options) error {
	switch options.Format {
	case FormatJSON:
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(result)
	case FormatYAML:
		jsonData, err := json.Marshal(result)
		if err != nil {
			return err
		}
		yamlData, err := yaml.JSONToYAML(jsonData)
		if err != nil {
			return err
		}
		_, err = w.Write(yamlData)
		return err
	case FormatTable, FormatWide:
		return writeTable(w, result, options)
	default:
		return fmt.Errorf("unsupported output format %q", options.Format)
	}
}

func writeTable(w io.Writer, result v1alpha1.Result, options Options) error {
	columns := append([]v1alpha1.ColumnHint(nil), options.Columns...)
	sort.SliceStable(columns, func(i, j int) bool { return columns[i].Order < columns[j].Order })
	filtered := columns[:0]
	for _, column := range columns {
		if column.Wide && options.Format != FormatWide {
			continue
		}
		filtered = append(filtered, column)
	}
	columns = filtered
	if len(columns) == 0 {
		columns = []v1alpha1.ColumnHint{
			{Header: "NAME", Path: "name", Type: v1alpha1.ColumnString},
			{Header: "KIND", Path: "kind", Type: v1alpha1.ColumnString},
		}
	}
	writer := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	defer writer.Flush()
	if !options.NoHeaders {
		for i, column := range columns {
			if i > 0 {
				_, _ = io.WriteString(writer, "\t")
			}
			_, _ = io.WriteString(writer, column.Header)
		}
		_, _ = io.WriteString(writer, "\n")
	}
	for _, item := range result.Items {
		root, err := itemRoot(item)
		if err != nil {
			return err
		}
		for i, column := range columns {
			if i > 0 {
				_, _ = io.WriteString(writer, "\t")
			}
			value, ok := lookup(root, column.Path)
			_, _ = io.WriteString(writer, formatValue(value, ok, column.Type))
		}
		_, _ = io.WriteString(writer, "\n")
	}
	return nil
}

func itemRoot(item v1alpha1.Item) (map[string]any, error) {
	var data map[string]any
	decoder := json.NewDecoder(bufio.NewReader(bytes.NewReader(item.Data)))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("decode %s item data: %w", item.Kind, err)
	}
	return map[string]any{"target": item.Target, "name": item.Name, "kind": item.Kind, "data": data}, nil
}

func lookup(root map[string]any, path string) (any, bool) {
	var current any = root
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok || current == nil {
			return nil, false
		}
	}
	return current, true
}

func formatValue(value any, ok bool, kind v1alpha1.ColumnType) string {
	if !ok {
		return "-"
	}
	switch kind {
	case v1alpha1.ColumnBytes, v1alpha1.ColumnBytesPerSecond:
		n, ok := uint64Value(value)
		if !ok {
			return "-"
		}
		formatted := formatBytes(n)
		if kind == v1alpha1.ColumnBytesPerSecond {
			return formatted + "/s"
		}
		return formatted
	case v1alpha1.ColumnDurationSeconds:
		seconds, ok := float64Value(value)
		if !ok {
			return "-"
		}
		return formatDuration(seconds)
	case v1alpha1.ColumnCount:
		switch items := value.(type) {
		case []any:
			return strconv.Itoa(len(items))
		default:
			return "-"
		}
	case v1alpha1.ColumnNamedBytesList:
		items, ok := value.([]any)
		if !ok || len(items) == 0 {
			return "-"
		}
		parts := make([]string, 0, len(items))
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			name, _ := item["name"].(string)
			size, sizeOK := uint64Value(item["sizeBytes"])
			if name == "" || !sizeOK {
				continue
			}
			parts = append(parts, name+":"+formatBytes(size))
		}
		if len(parts) == 0 {
			return "-"
		}
		return strings.Join(parts, ",")
	case v1alpha1.ColumnBoolean:
		if boolean, ok := value.(bool); ok {
			return strconv.FormatBool(boolean)
		}
		return "-"
	case v1alpha1.ColumnDecimal, v1alpha1.ColumnPercent:
		number, ok := float64Value(value)
		if !ok {
			return "-"
		}
		formatted := strconv.FormatFloat(number, 'f', 2, 64)
		if kind == v1alpha1.ColumnPercent {
			return formatted + "%"
		}
		return formatted
	default:
		return fmt.Sprint(value)
	}
}

func uint64Value(value any) (uint64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseUint(number.String(), 10, 64)
		return parsed, err == nil
	case float64:
		return uint64(number), number >= 0
	case int:
		return uint64(number), number >= 0
	case uint64:
		return number, true
	default:
		return 0, false
	}
}

func float64Value(value any) (float64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(number.String(), 64)
		return parsed, err == nil
	case float64:
		return number, true
	case int:
		return float64(number), true
	default:
		return 0, false
	}
}

func formatBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	exponent := int(math.Log(float64(value)) / math.Log(unit))
	if exponent > 6 {
		exponent = 6
	}
	prefix := "KMGTPE"[exponent-1]
	return fmt.Sprintf("%.1f %ciB", float64(value)/math.Pow(unit, float64(exponent)), prefix)
}

func formatDuration(seconds float64) string {
	duration := time.Duration(seconds * float64(time.Second))
	days := duration / (24 * time.Hour)
	duration %= 24 * time.Hour
	if days > 0 {
		return fmt.Sprintf("%dd%dh", days, duration/time.Hour)
	}
	if duration >= time.Hour {
		return fmt.Sprintf("%dh%dm", duration/time.Hour, (duration%time.Hour)/time.Minute)
	}
	if duration >= time.Minute {
		return fmt.Sprintf("%dm", duration/time.Minute)
	}
	return fmt.Sprintf("%ds", duration/time.Second)
}
