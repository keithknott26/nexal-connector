package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"nexal/connector/internal/inference"
	"nexal/connector/internal/sysinfo"
)

// `nexal inference preflight` is the first half of the "Add AI Inference Model"
// feature: a READ-ONLY check of what this Mac and the peers on its neXal mesh
// could run. It installs nothing, downloads nothing and opens no network
// connection of its own. Everything it reports comes from three local sources:
//
//   - the connector's own local API (GET /v1/status over loopback, the same
//     authenticated call `nexal status` makes): owner memory cap and reserve,
//     telemetry, mesh peers (path, latency, PQ state, path flaps, measured
//     bandwidth) and the details other hosts reported (chip, OS, memory, disk);
//   - sysinfo for this Mac's own chip, OS and free disk;
//   - the MLX runtime's metadata-only `probe`, run only after the owner policy's
//     SHA-256 pins for the interpreter and runtime files match.
//
// All of the logic lives in internal/inference, where it is tested with fake
// sources; this file only wires the real ones. See CLI-CONTRACT.md.
func inferenceCommand(ctx context.Context, args []string) error {
	const usage = "usage: nexal inference preflight|pin|install|status|verify|remove (see connector/CLI-CONTRACT.md)"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "preflight":
		return inferencePreflight(ctx, args)
	case "pin":
		return inferencePin(ctx, args[1:])
	case "install":
		return inferenceInstall(ctx, args[1:])
	case "status":
		return inferenceStatus(ctx, args[1:])
	case "verify":
		return inferenceVerify(ctx, args[1:])
	case "remove":
		return inferenceRemove(ctx, args[1:])
	}
	return errors.New(usage)
}

func inferencePreflight(ctx context.Context, args []string) error {
	const usage = "usage: nexal inference preflight [--json] [--runtime-owner-policy absolute-path] [--config absolute-path]"
	if len(args) == 0 || args[0] != "preflight" {
		return errors.New(usage)
	}
	f, path, err := flags("inference")
	if err != nil {
		return err
	}
	asJSON := f.Bool("json", false, "print the report as one JSON document")
	policy := f.String("runtime-owner-policy", "", "absolute path of the MLX runtime owner policy (default: inference/owner-policy.json beside config.json)")
	if err := parse(f, args[1:], path); err != nil {
		return err
	}
	policyPath := *policy
	if policyPath == "" {
		policyPath = filepath.Join(filepath.Dir(*path), "inference", "owner-policy.json")
	} else if !filepath.IsAbs(policyPath) {
		return errors.New("--runtime-owner-policy must be an absolute path")
	}
	// The embedded catalog is unpinned; <config dir>/inference/pins.json (written by
	// `nexal inference pin`) is merged over it. A broken overlay never breaks this
	// read-only check: it falls back to the embedded catalog and says so.
	catalog, overlayErr := inference.LoadCatalogWithPins(filepath.Join(filepath.Dir(*path), "inference"))
	if overlayErr != nil {
		var err error
		if catalog, err = inference.LoadCatalog(); err != nil {
			return err
		}
	}

	// A connector that is not running is a finding, not a failure: the report
	// says so and still describes this Mac from local facts.
	raw, fetchErr := localFetch(ctx, *path, "GET", "status", nil, 4<<20)
	info := sysinfo.NewCollector().Collect(ctx)
	src := inference.NewStatusSource(raw, fetchErr, inference.LocalInfo{
		Name: info.Name, OS: info.OS, Chip: info.Chip, MemoryBytes: info.MemoryBytes,
		DiskFreeBytes: info.DiskFreeBytes, DiskKnown: info.DiskFreeBytes > 0,
	})
	engine := inference.Engine{Hosts: src, Links: src, Catalog: catalog,
		Runtime: inference.FileProber{OwnerPolicyPath: policyPath}}
	report, err := engine.Run(ctx)
	if err != nil {
		return err
	}
	if overlayErr != nil {
		report.Gaps = append(report.Gaps, "The pins overlay (inference/pins.json) was ignored: "+overlayErr.Error())
	}
	if *asJSON {
		return emit(report)
	}
	_, err = io.WriteString(os.Stdout, inference.RenderText(report))
	return err
}
