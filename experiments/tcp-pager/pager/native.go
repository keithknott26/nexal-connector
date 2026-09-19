package pager

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// Native runs only the explicitly selected, locally built research helper.
// The helper owns cache frames; this broker owns the authenticated page session.
func Native(ctx context.Context, c *Client, slots int, helper string) (Report, error) {
	r := Report{Mode: "native HVF CPU-fault pager", LogicalBytes: c.Pages() * PageSize, CachePayloadLimitBytes: slots * PageSize}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return r, errors.New("native mode requires an Apple-silicon Mac")
	}
	if slots < 1 || slots > 16 || slots >= c.Pages() {
		return r, errors.New("invalid cache size")
	}
	path, err := filepath.Abs(helper)
	if err != nil {
		return r, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return r, err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0022 != 0 || fi.Mode().Perm()&0100 == 0 {
		return r, errors.New("helper must be a regular owner-executable file, not group/world writable or a symlink")
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, path, strconv.Itoa(c.Pages()), strconv.Itoa(slots))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	cmd.WaitDelay = 2 * time.Second
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return r, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return r, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return r, err
	}
	protocolErr := Broker(ctx, c, out, in, &r)
	in.Close()
	if protocolErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if protocolErr != nil {
		return r, protocolErr
	}
	if waitErr != nil {
		return r, fmt.Errorf("native helper failed: %w", waitErr)
	}
	r.Transport = c.Stats()
	r.NativeHVFExecuted = true
	r.Verified = true
	return r, nil
}

// Broker is separately testable on Linux. A completion packet is accepted only
// after an actual full writeback and remote reread, not merely a child assertion.
func Broker(ctx context.Context, c *Client, reader io.Reader, writer io.Writer, r *Report) error {
	limit := c.Pages()*8 + 32
	for requests := 0; requests < limit; requests++ {
		var op [1]byte
		if _, err := io.ReadFull(reader, op[:]); err != nil {
			return err
		}
		if op[0] == 'D' {
			var counts [32]byte
			if _, err := io.ReadFull(reader, counts[:]); err != nil {
				return err
			}
			faults := binary.BigEndian.Uint64(counts[0:8])
			evictions := binary.BigEndian.Uint64(counts[8:16])
			verified := binary.BigEndian.Uint64(counts[16:24])
			peak := binary.BigEndian.Uint64(counts[24:32])
			stats := c.Stats()
			if verified != uint64(c.Pages()*PageSize) || faults != uint64(2*c.Pages()) ||
				peak != uint64(r.CachePayloadLimitBytes/PageSize) ||
				evictions != uint64(2*(c.Pages()-r.CachePayloadLimitBytes/PageSize)) ||
				stats.Puts != uint64(c.Pages()) || stats.Gets != uint64(2*c.Pages()) {
				return errors.New("native completion counters do not match bounded workload")
			}
			r.VerifiedBytes = int(verified)
			r.Cache = CacheStats{Faults: faults, Evictions: evictions, PeakResidentPages: int(peak)}
			return nil
		}
		if op[0] != 'G' && op[0] != 'P' {
			return errors.New("unknown native IPC opcode")
		}
		var id [4]byte
		if _, err := io.ReadFull(reader, id[:]); err != nil {
			return err
		}
		page := int(binary.BigEndian.Uint32(id[:]))
		var data []byte
		var err error
		if op[0] == 'G' {
			data, err = c.Get(ctx, page)
		} else {
			data = make([]byte, PageSize)
			if _, err = io.ReadFull(reader, data); err == nil {
				err = c.Put(ctx, page, data)
			}
		}
		if err != nil {
			return fmt.Errorf("native backing request failed: %w", err)
		}
		if err = writeFull(writer, []byte{0}); err != nil {
			return err
		}
		if op[0] == 'G' {
			if err = writeFull(writer, data); err != nil {
				return err
			}
		}
	}
	return errors.New("native IPC operation limit exceeded")
}
