package raid

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"toolctl/api/v1alpha1"
	"toolctl/internal/core"
)

type locateProcesses struct {
	calls     []core.ProcessSpec
	failStart bool
	stopped   bool
}

func (*locateProcesses) LookPath(string) (string, error) { return "", errors.New("not found") }
func (p *locateProcesses) Run(ctx context.Context, s core.ProcessSpec) core.ProcessResult {
	p.calls = append(p.calls, s)
	if s.Args[0] == "-PDLocate" {
		if s.Args[1] == "-stop" {
			p.stopped = ctx.Err() == nil
		}
		if s.Args[1] == "-start" && p.failStart {
			return core.ProcessResult{Stdout: []byte("Exit Code: 0x01")}
		}
		return core.ProcessResult{Stdout: []byte("Exit Code: 0x00")}
	}
	return core.ProcessResult{Stdout: []byte("Adapter #0\nProduct Name: test\nVirtual Drive: 0\nState: Optimal\nEnclosure Device ID: 1\nSlot Number: 2\nDevice Id: 6\nFirmware state: Failed\nEnclosure Device ID: 252\nSlot Number: 0\nDevice Id: 0\nFirmware state: Online, Spun Up\n")}
}

type locateWaiter struct {
	duration time.Duration
	cancel   context.CancelFunc
}

func (w *locateWaiter) Wait(_ context.Context, d time.Duration) error {
	w.duration = d
	if w.cancel != nil {
		w.cancel()
	}
	return nil
}

func TestServiceLifecycle(t *testing.T) {
	for _, scenario := range []string{"normal", "cancel", "render-failure", "start-failure"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "MegaCli64")
			if e := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0700); e != nil {
				t.Fatal(e)
			}
			if e := saveTools(dir, map[string]string{"megacli": binary}); e != nil {
				t.Fatal(e)
			}
			files := newFakeFS()
			files.dirs["/sys/bus/pci/devices"] = []string{"0000:01:00.0"}
			files.files["/sys/bus/pci/devices/0000:01:00.0/class"] = "0x010400"
			files.files["/sys/bus/pci/devices/0000:01:00.0/vendor"] = "0x1000"
			processes := &locateProcesses{failStart: scenario == "start-failure"}
			waiter := &locateWaiter{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel" {
				waiter.cancel = cancel
			}
			r := &Runner{deps: core.Dependencies{Files: files, Processes: processes, Waiter: waiter, ConfigDirectory: dir, Hostname: func() (string, error) { return "host1", nil }}}
			emissions := 0
			err := r.ExecuteStream(ctx, v1alpha1.Operation{Capability: capabilityDisks, Options: map[string]json.RawMessage{"service": json.RawMessage("true")}}, func(out core.RunOutput) error {
				emissions++
				if len(out.Items) != 1 {
					t.Fatalf("expected only faulty disk: %+v", out)
				}
				var d Disk
				_ = json.Unmarshal(out.Items[0].Data, &d)
				if d.Slot != "2" || d.Enclosure != "1" || d.Host != "host1" {
					t.Fatalf("wrong disk %+v", d)
				}
				if scenario == "render-failure" {
					return errors.New("broken output")
				}
				return nil
			})
			if (scenario == "render-failure" || scenario == "start-failure") && err == nil {
				t.Fatal("expected failure")
			}
			if !processes.stopped {
				t.Fatal("cleanup did not run with live context")
			}
			if scenario == "normal" && waiter.duration != 10*time.Minute {
				t.Fatalf("wait = %v", waiter.duration)
			}
			if emissions != 2 {
				t.Fatalf("emissions %d", emissions)
			}
			for _, c := range processes.calls {
				if c.Args[0] == "-PDLocate" && strings.Join(c.Args[2:], " ") != "-physdrv[1:2] -a0 -NoLog" {
					t.Fatalf("wrong target %+v", c)
				}
			}
		})
	}
}

func TestServiceTargets(t *testing.T) {
	if megaCLISelector(Disk{Backend: "megacli", Controller: "0", Enclosure: "1", Slot: "2"}) != "-physdrv '[1:2]' -a0" {
		t.Fatal("selector")
	}
	if megaCLISelector(Disk{Backend: "megacli", Controller: "ALL", Enclosure: "1", Slot: "2"}) != "" {
		t.Fatal("wildcard allowed")
	}
	for _, state := range []string{"Online, Spun Up", "Unconfigured(good), Spun Up", "Hotspare"} {
		if abnormalDisk(Disk{VendorState: state}) {
			t.Fatalf("healthy %s selected", state)
		}
	}
	for _, state := range []string{"Failed", "Rebuild", "Unconfigured(bad)"} {
		if !abnormalDisk(Disk{VendorState: state}) {
			t.Fatalf("abnormal %s omitted", state)
		}
	}
}
