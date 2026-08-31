package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	resultbuilder "toolctl/internal/result"
)

func TestTableAndWideColumns(t *testing.T) {
	item, err := resultbuilder.NewItem("HostInfo", "node-1", "", map[string]any{"memory": map[string]any{"totalBytes": uint64(8 << 30)}, "kernel": "6.1"})
	if err != nil {
		t.Fatal(err)
	}
	result := v1alpha1.Result{Items: []v1alpha1.Item{item}}
	columns := []v1alpha1.ColumnHint{
		{Header: "NAME", Path: "name", Type: v1alpha1.ColumnString, Order: 1},
		{Header: "MEMORY", Path: "data.memory.totalBytes", Type: v1alpha1.ColumnBytes, Order: 2},
		{Header: "KERNEL", Path: "data.kernel", Type: v1alpha1.ColumnString, Wide: true, Order: 3},
	}
	var table bytes.Buffer
	if err := Write(&table, result, Options{Format: FormatTable, Columns: columns}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(table.String(), "KERNEL") || !strings.Contains(table.String(), "8.0 GiB") {
		t.Fatalf("unexpected table:\n%s", table.String())
	}
	var wide bytes.Buffer
	if err := Write(&wide, result, Options{Format: FormatWide, Columns: columns}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wide.String(), "KERNEL") {
		t.Fatalf("unexpected wide table:\n%s", wide.String())
	}
}

func TestJSONKeepsEmptyArrays(t *testing.T) {
	result := v1alpha1.Result{
		APIVersion: v1alpha1.APIVersion, Kind: "Result", Status: v1alpha1.StatusSuccess,
		Items: []v1alpha1.Item{}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{},
		Metadata: v1alpha1.ResultMetadata{GeneratedAt: time.Unix(0, 0).UTC()},
	}
	var output bytes.Buffer
	if err := Write(&output, result, Options{Format: FormatJSON}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"items": []`, `"warnings": []`, `"errors": []`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %s in:\n%s", expected, output.String())
		}
	}
}

func TestYAMLKeepsLargeIntegerPrecision(t *testing.T) {
	item, err := resultbuilder.NewItem("Counter", "one", "", struct {
		Value uint64 `json:"value"`
	}{Value: 9_007_199_254_740_993})
	if err != nil {
		t.Fatal(err)
	}
	result := v1alpha1.Result{Items: []v1alpha1.Item{item}, Warnings: []v1alpha1.Diagnostic{}, Errors: []v1alpha1.TargetError{}}
	var output bytes.Buffer
	if err := Write(&output, result, Options{Format: FormatYAML}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "9007199254740993") {
		t.Fatalf("integer precision changed:\n%s", output.String())
	}
}

func TestNamedBytesList(t *testing.T) {
	item, err := resultbuilder.NewItem("HostInfo", "node", "", map[string]any{
		"disks": []map[string]any{{"name": "sda", "sizeBytes": uint64(1 << 40)}, {"name": "nvme0n1", "sizeBytes": uint64(2 << 40)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = Write(&output, v1alpha1.Result{Items: []v1alpha1.Item{item}}, Options{
		Format: FormatTable, Columns: []v1alpha1.ColumnHint{{Header: "DISKS", Path: "data.disks", Type: v1alpha1.ColumnNamedBytesList}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "sda:1.0 TiB,nvme0n1:2.0 TiB") {
		t.Fatalf("output = %s", output.String())
	}
}

func TestPerformanceValueFormats(t *testing.T) {
	item, err := resultbuilder.NewItem("NetworkStat", "eth0", "", map[string]any{"rate": 1 << 20, "util": 12.345})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = Write(&output, v1alpha1.Result{Items: []v1alpha1.Item{item}}, Options{
		Format: FormatTable,
		Columns: []v1alpha1.ColumnHint{
			{Header: "RATE", Path: "data.rate", Type: v1alpha1.ColumnBytesPerSecond},
			{Header: "UTIL", Path: "data.util", Type: v1alpha1.ColumnPercent},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1.0 MiB/s") || !strings.Contains(output.String(), "12.35%") {
		t.Fatalf("output = %s", output.String())
	}
}
