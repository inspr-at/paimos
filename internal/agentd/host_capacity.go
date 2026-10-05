// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/hostcapacity"
)

var ErrHostCapacity = errors.New("waiting for host capacity")

// Commands are fixed OS probes with bounded output and one shared deadline.
// There is no environment dump, process listing or input-content collection.
func hostProbe(ctx context.Context, path string, args ...string) string {
	capture := &probeCapture{max: 64 << 10}
	command := exec.CommandContext(ctx, path, args...)
	command.Stdout = capture
	command.Stderr = &probeCapture{max: 4 << 10}
	if command.Run() != nil || capture.overflow {
		return ""
	}
	return string(capture.buf)
}
func hostNumber(value string) *float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return nil
	}
	return &n
}
func sampleHost(ctx context.Context, activity bool) hostcapacity.Signals {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s := hostcapacity.Signals{Cores: runtime.NumCPU(), MemoryPressure: "unknown", Power: "unknown", Thermal: "unknown"}
	if runtime.GOOS == "darwin" {
		load := strings.Fields(hostProbe(ctx, "/usr/sbin/sysctl", "-n", "vm.loadavg"))
		if len(load) >= 2 {
			s.Load = hostNumber(load[1])
		}
		pressure := strings.TrimSpace(hostProbe(ctx, "/usr/sbin/sysctl", "-n", "kern.memorystatus_vm_pressure_level"))
		if pressure == "1" {
			s.MemoryPressure = "normal"
		} else if pressure == "2" || pressure == "4" {
			s.MemoryPressure = "high"
		}
		if total := hostNumber(hostProbe(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")); total != nil {
			gb := *total / (1 << 30)
			s.MemoryTotalGB = &gb
		}
		if s.MemoryTotalGB != nil {
			stats := hostProbe(ctx, "/usr/bin/vm_stat")
			var pageSize, free float64
			for _, line := range strings.Split(stats, "\n") {
				if strings.Contains(line, "page size of") {
					fields := strings.Fields(line)
					for i, word := range fields {
						if word == "of" && i+1 < len(fields) {
							if n := hostNumber(fields[i+1]); n != nil {
								pageSize = *n
							}
						}
					}
				}
				if strings.HasPrefix(line, "Pages free:") || strings.HasPrefix(line, "Pages inactive:") || strings.HasPrefix(line, "Pages speculative:") {
					parts := strings.Split(line, ":")
					if len(parts) == 2 {
						if n := hostNumber(strings.TrimSuffix(strings.TrimSpace(parts[1]), ".")); n != nil {
							free += *n
						}
					}
				}
			}
			if pageSize > 0 {
				used := *s.MemoryTotalGB - free*pageSize/(1<<30)
				if used >= 0 {
					s.MemoryUsedGB = &used
				}
			}
		}
		batt := hostProbe(ctx, "/usr/bin/pmset", "-g", "batt")
		if strings.Contains(batt, "AC Power") {
			s.Power = "plugged_in"
		} else if strings.Contains(batt, "Battery Power") {
			s.Power = "battery"
		}
		therm := hostProbe(ctx, "/usr/bin/pmset", "-g", "therm")
		if strings.Contains(therm, "No thermal warning") {
			s.Thermal = "normal"
		} else if strings.Contains(therm, "CPU_Speed_Limit") {
			for _, line := range strings.Split(therm, "\n") {
				if strings.Contains(line, "CPU_Speed_Limit") {
					parts := strings.Split(line, "=")
					if len(parts) == 2 {
						if n := hostNumber(parts[1]); n != nil {
							s.Thermal = "normal"
							if *n < 100 {
								s.Thermal = "hot"
							}
						}
					}
				}
			}
		}
		if activity {
			// IOHIDSystem exposes only idle nanoseconds. Never query key events.
			report := hostProbe(ctx, "/usr/sbin/ioreg", "-r", "-c", "IOHIDSystem", "-d", "1")
			for _, line := range strings.Split(report, "\n") {
				if strings.Contains(line, "\"HIDIdleTime\"") {
					parts := strings.Split(line, "=")
					if len(parts) == 2 {
						if idle := hostNumber(parts[1]); idle != nil {
							active := *idle < 60e9
							s.InputActive = &active
						}
					}
				}
			}
		}
	} else if runtime.GOOS == "linux" {
		if data, err := readHostFile("/proc/loadavg"); err == nil {
			values := strings.Fields(data)
			if len(values) > 0 {
				s.Load = hostNumber(values[0])
			}
		}
		if data, err := readHostFile("/proc/meminfo"); err == nil {
			values := map[string]float64{}
			for _, line := range strings.Split(data, "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if n := hostNumber(fields[1]); n != nil {
						values[fields[0]] = *n
					}
				}
			}
			total, available := values["MemTotal:"], values["MemAvailable:"]
			if total > 0 {
				totalGB, usedGB := total/(1<<20), (total-available)/(1<<20)
				s.MemoryTotalGB = &totalGB
				s.MemoryUsedGB = &usedGB
				s.MemoryPressure = "normal"
				if available/total < .1 {
					s.MemoryPressure = "high"
				}
			}
		}
	}
	return s
}
func readHostFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buf := make([]byte, 16<<10)
	n, err := file.Read(buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}
func (r *Remote) HostCapacity(ctx context.Context) (hostcapacity.View, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out hostcapacity.View
	err := r.Client.Do(ctx, "POST", "/api/agent-pairing/self/capacity", sampleHost(ctx, false), &out)
	if err != nil {
		return out, err
	}
	if out.Policy.ConsiderActivity {
		err = r.Client.Do(ctx, "POST", "/api/agent-pairing/self/capacity", sampleHost(ctx, true), &out)
	}
	return out, err
}

// dispatchMu serializes this check with local starts. The server repeats it in
// the fenced claim transaction, counting all accounts on this computer.
func (s *Supervisor) checkHostCapacity(ctx context.Context) error {
	reporter, ok := s.api.(interface {
		HostCapacity(context.Context) (hostcapacity.View, error)
	})
	if !ok {
		return nil
	} // In-process API adapters without a paired computer.
	view, err := reporter.HostCapacity(ctx)
	if err != nil {
		return err
	}
	if view.Reason != "" {
		return errors.Join(ErrHostCapacity, errors.New(view.Reason))
	}
	return nil
}
