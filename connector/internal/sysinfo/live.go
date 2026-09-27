package sysinfo

import (
	"bytes"
	"context"
	"errors"
	"math"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 256<<10 {
		return 0, errors.New("diagnostic output limit exceeded")
	}
	return b.Buffer.Write(p)
}

var cpuPattern = regexp.MustCompile(`CPU usage:\s*([0-9.]+)% user,\s*([0-9.]+)% sys,\s*([0-9.]+)% idle`)
var idlePattern = regexp.MustCompile(`(?m)^\s*"HIDIdleTime"\s*=\s*([0-9]{1,20})\s*$`)
var pagePattern = regexp.MustCompile(`page size of ([0-9]+) bytes`)
var freePattern = regexp.MustCompile(`(?m)^Pages (?:free|speculative):\s*([0-9]+)\.`)

func parseCPU(out []byte) *float64 {
	matches := cpuPattern.FindAllSubmatch(out, -1)
	if len(matches) == 0 {
		return nil
	}
	m := matches[len(matches)-1]
	idle, err := strconv.ParseFloat(string(m[3]), 64)
	if err != nil || idle < 0 || idle > 100 {
		return nil
	}
	usage := 100 - idle
	return &usage
}
func parseAvailable(out []byte, total uint64) *uint64 {
	page := pagePattern.FindSubmatch(out)
	counts := freePattern.FindAllSubmatch(out, -1)
	if len(page) != 2 || len(counts) != 2 {
		return nil
	}
	size, err := strconv.ParseUint(string(page[1]), 10, 64)
	if err != nil || (size != 4096 && size != 16384) {
		return nil
	}
	var n uint64
	for _, m := range counts {
		v, e := strconv.ParseUint(string(m[1]), 10, 64)
		if e != nil || v > total/size {
			return nil
		}
		n += v * size
	}
	if n > total {
		return nil
	}
	return &n
}
func parseBackup(out []byte) string {
	// Never transmit the backup volume, path or computer name.
	name := filepath.Base(strings.TrimSpace(string(out)))
	name = strings.TrimSuffix(name, ".backup")
	at, err := time.ParseInLocation("2006-01-02-150405", name, time.Local)
	if err != nil || at.After(time.Now().Add(time.Minute)) {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
func parseLoadAverage(out []byte) (*float64, *float64, *float64) {
	fields := strings.Fields(string(out))
	if len(out) > 256 || len(fields) != 5 || fields[0] != "{" || fields[4] != "}" {
		return nil, nil, nil
	}
	var loads [3]float64
	for i := range loads {
		value, err := strconv.ParseFloat(fields[i+1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1_000_000 {
			return nil, nil, nil
		}
		loads[i] = value
	}
	return &loads[0], &loads[1], &loads[2]
}

func (c *Collector) collectLive(ctx context.Context, info *Info) {
	if runtime.GOOS != "darwin" {
		return
	}
	if out, err := c.Run(ctx, "/usr/sbin/sysctl", "-n", "vm.loadavg"); err == nil {
		info.LoadAverage1m, info.LoadAverage5m, info.LoadAverage15m = parseLoadAverage(out)
	}
	if out, err := c.Run(ctx, "/usr/bin/top", "-l", "2", "-s", "1", "-n", "0"); err == nil {
		info.CPUUsagePercent = parseCPU(out)
	}
	if out, err := c.Run(ctx, "/usr/bin/vm_stat"); err == nil {
		info.MemoryAvailableBytes = parseAvailable(out, info.MemoryBytes)
		if info.MemoryAvailableBytes != nil {
			used := info.MemoryBytes - *info.MemoryAvailableBytes
			info.MemoryUsedBytes = &used
		}
	}
	if out, err := c.Run(ctx, "/usr/sbin/ioreg", "-r", "-c", "IOHIDSystem", "-d", "1"); err == nil {
		if m := idlePattern.FindSubmatch(out); len(m) == 2 {
			if n, e := strconv.ParseUint(string(m[1]), 10, 64); e == nil {
				secs := n / uint64(time.Second)
				info.IdleSeconds = &secs
			}
		}
	}
	if out, err := c.Run(ctx, "/usr/bin/tmutil", "latestbackup"); err == nil {
		info.LastTimeMachineBackupAt = parseBackup(out)
	}
}
