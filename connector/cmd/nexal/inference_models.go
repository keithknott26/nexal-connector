package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"nexal/connector/internal/inference"
	"nexal/connector/internal/sysinfo"
)

// The model-management half of "Add AI Inference Model":
//
//	nexal inference pin <model-id> --revision <40hex> [--repo owner/name] [--json]
//	nexal inference install <model-id> [--json]
//	nexal inference status [--json]
//	nexal inference verify <model-id> [--json]
//	nexal inference remove <model-id> [--yes] [--json]
//
// Only pin and install open network connections, and only to huggingface.co or
// *.hf.co over https (internal/inference/hf.go). None of them writes
// owner-policy.json or runtime-config.json. See CLI-CONTRACT.md.

func inferenceDirOf(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "inference")
}

// parseModelArgs accepts `<id> [flags]` (the documented form) or `[flags] <id>`.
// needID=false means the command takes no model id.
func parseModelArgs(name string, args []string, needID bool, define func(f *flag.FlagSet)) (string, *flag.FlagSet, *string, error) {
	f, path, err := flags("inference " + name)
	if err != nil {
		return "", nil, nil, err
	}
	if define != nil {
		define(f)
	}
	id := ""
	if needID && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id, args = args[0], args[1:]
	}
	if err := f.Parse(args); err != nil {
		return "", nil, nil, &codedError{code: "invalid_arguments", err: errors.New("invalid command flags; consult connector/CLI-CONTRACT.md")}
	}
	switch {
	case needID && id == "" && f.NArg() == 1:
		id = f.Arg(0)
	case f.NArg() != 0:
		return "", nil, nil, &codedError{code: "invalid_arguments", err: errors.New("unexpected positional arguments")}
	}
	if needID && id == "" {
		return "", nil, nil, &codedError{code: "invalid_arguments", err: fmt.Errorf("usage: nexal inference %s <model-id> [flags]", name)}
	}
	if !filepath.IsAbs(*path) {
		return "", nil, nil, &codedError{code: "invalid_arguments", err: errors.New("--config must be an absolute path")}
	}
	return id, f, path, nil
}

// coded turns an inference.Error into the CLI's coded error.
func coded(err error) error {
	if err == nil {
		return nil
	}
	e := inference.AsError(err)
	return &codedError{code: e.Code, err: e}
}

func writeLine(w io.Writer, format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }

func gibText(b int64) string { return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30)) }

// ---- pin ----

func inferencePin(ctx context.Context, args []string) error {
	var revision, repo *string
	var asJSON *bool
	id, _, path, err := parseModelArgs("pin", args, true, func(f *flag.FlagSet) {
		revision = f.String("revision", "", "immutable 40-hex commit to pin (required)")
		repo = f.String("repo", "", "Hugging Face repository owner/name (default: the catalog's)")
		asJSON = f.Bool("json", false, "print one JSON document")
	})
	if err != nil {
		return err
	}
	if *revision == "" {
		return &codedError{code: "invalid_arguments", err: errors.New("--revision <40-hex commit> is required")}
	}
	client, err := inference.NewHFClient(inference.HFOptions{})
	if err != nil {
		return coded(err)
	}
	base, err := inference.LoadCatalog()
	if err != nil {
		return err
	}
	p := &inference.Pinner{InferenceDir: inferenceDirOf(*path), Catalog: base, Client: client}
	if !*asJSON {
		last := ""
		p.Progress = func(file string, n, total int64) {
			if file != last && n > 0 {
				last = file
				writeLine(os.Stderr, "hashing %s (%s)", file, gibText(total))
			}
		}
	}
	res, perr := p.Pin(ctx, id, *revision, *repo)
	if *asJSON {
		if perr == nil || len(res.Blockers) > 0 {
			if err := emit(res); err != nil {
				return err
			}
		}
		return coded(perr)
	}
	w := os.Stdout
	if perr != nil {
		for _, b := range res.Blockers {
			writeLine(w, "BLOCKER %s: %s (%s)", b.File, b.Key, b.Detail)
		}
		for _, l := range res.Commit {
			writeLine(w, "%s", l)
		}
		if e := inference.AsError(perr); e.Fix != "" {
			writeLine(w, "Fix: %s", e.Fix)
		}
		return coded(perr)
	}
	writeLine(w, "Pinned %s from %s at %s", res.ModelID, res.HFRepo, res.Revision)
	writeLine(w, "License: %s   model_type: %s   %d files, %s", res.License, res.ModelType, len(res.Files), gibText(res.TotalBytes))
	for _, f := range res.Files {
		writeLine(w, "  %-36s %12d  %s", f.Name, f.Size, f.SHA256)
	}
	for _, l := range res.Commit {
		writeLine(w, "%s", l)
	}
	return nil
}

// ---- install ----

func inferenceInstall(ctx context.Context, args []string) error {
	var asJSON *bool
	id, _, path, err := parseModelArgs("install", args, true, func(f *flag.FlagSet) {
		asJSON = f.Bool("json", false, "print newline-delimited JSON events")
	})
	if err != nil {
		return err
	}
	infDir := inferenceDirOf(*path)
	catalog, err := inference.LoadCatalogWithPins(infDir)
	if err != nil {
		return coded(err)
	}
	client, err := inference.NewHFClient(inference.HFOptions{})
	if err != nil {
		return coded(err)
	}
	preflight := func(ctx context.Context) (inference.Report, error) {
		raw, fetchErr := localFetch(ctx, *path, "GET", "status", nil, 4<<20)
		info := sysinfo.NewCollector().Collect(ctx)
		src := inference.NewStatusSource(raw, fetchErr, inference.LocalInfo{
			Name: info.Name, OS: info.OS, Chip: info.Chip, MemoryBytes: info.MemoryBytes,
			DiskFreeBytes: info.DiskFreeBytes, DiskKnown: info.DiskFreeBytes > 0,
		})
		engine := inference.Engine{Hosts: src, Links: src, Catalog: catalog,
			Runtime: inference.FileProber{OwnerPolicyPath: filepath.Join(infDir, "owner-policy.json")}}
		return engine.Run(ctx)
	}
	enc := json.NewEncoder(os.Stdout)
	pr := &plainInstall{w: os.Stdout}
	in := &inference.Installer{InferenceDir: infDir, Catalog: catalog, Client: client, Preflight: preflight,
		Emit: func(ev any) {
			if *asJSON {
				_ = enc.Encode(ev)
			} else {
				pr.render(ev)
			}
		}}
	_, ierr := in.Install(ctx, id)
	return coded(ierr)
}

// plainInstall renders install events as text. Progress is shown per 10% step.
type plainInstall struct {
	w    io.Writer
	step map[string]int64
}

func (p *plainInstall) render(ev any) {
	switch e := ev.(type) {
	case inference.StartEvent:
		writeLine(p.w, "Installing %s (%s to download)", e.ModelID, gibText(e.TotalBytes))
	case inference.ProgressEvent:
		if p.step == nil {
			p.step = map[string]int64{}
		}
		if e.Total <= 0 {
			return
		}
		s := e.Bytes * 10 / e.Total
		if prev, seen := p.step[e.File]; !seen || s > prev || e.Bytes == e.Total {
			if seen && prev == s && e.Bytes != e.Total {
				return
			}
			p.step[e.File] = s
			writeLine(p.w, "  %s: %d%% (overall %s of %s)", e.File, e.Bytes*100/e.Total, gibText(e.OverallBytes), gibText(e.OverallTotal))
		}
	case inference.VerifiedEvent:
		writeLine(p.w, "  verified %s", e.File)
	case inference.DoneEvent:
		writeLine(p.w, "Installed %s to %s", e.ModelID, e.Dir)
		writeLine(p.w, "State: %s   manifest SHA-256: %s", e.State, e.ManifestSHA256)
		for _, l := range e.HowToUse {
			writeLine(p.w, "%s", l)
		}
	case inference.ErrorEvent:
		writeLine(p.w, "Install failed [%s]: %s", e.Code, e.Message)
		if e.Fix != "" {
			writeLine(p.w, "Fix: %s", e.Fix)
		}
	}
}

// ---- status / verify / remove ----

func catalogNames(infDir string) map[string]string {
	names := map[string]string{}
	if c, err := inference.LoadCatalogWithPins(infDir); err == nil {
		for _, e := range c.Entries {
			names[e.ID] = e.DisplayName
		}
	} else if c, err := inference.LoadCatalog(); err == nil {
		for _, e := range c.Entries {
			names[e.ID] = e.DisplayName
		}
	}
	return names
}

func inferenceStatus(ctx context.Context, args []string) error {
	var asJSON *bool
	_, _, path, err := parseModelArgs("status", args, false, func(f *flag.FlagSet) {
		asJSON = f.Bool("json", false, "print one JSON document")
	})
	if err != nil {
		return err
	}
	infDir := inferenceDirOf(*path)
	rep, err := inference.Status(ctx, infDir, catalogNames(infDir))
	if err != nil {
		return coded(err)
	}
	if *asJSON {
		return emit(rep)
	}
	if len(rep.Models) == 0 {
		writeLine(os.Stdout, "No models are installed.")
	}
	for _, m := range rep.Models {
		writeLine(os.Stdout, "%s: %s, revision %s, %s on disk, installed %s", m.ModelID, m.State, m.Revision, gibText(m.BytesOnDisk), m.InstalledAt)
		for _, pr := range m.Problems {
			writeLine(os.Stdout, "  problem: %s", pr)
		}
	}
	for _, i := range rep.Incomplete {
		writeLine(os.Stdout, "%s: incomplete download (%s kept); run `nexal inference install %s` to resume", i.ModelID, gibText(i.BytesOnDisk), i.ModelID)
	}
	return nil
}

func inferenceVerify(ctx context.Context, args []string) error {
	var asJSON *bool
	id, _, path, err := parseModelArgs("verify", args, true, func(f *flag.FlagSet) {
		asJSON = f.Bool("json", false, "print one JSON document")
	})
	if err != nil {
		return err
	}
	res, err := inference.CheckModel(ctx, inferenceDirOf(*path), id, true)
	if err != nil {
		return coded(err)
	}
	if *asJSON {
		if err := emit(res); err != nil {
			return err
		}
	} else {
		for _, f := range res.Files {
			writeLine(os.Stdout, "  %-40s %s", f.Name, f.Status)
		}
		if res.OK {
			writeLine(os.Stdout, "%s: every file matches its install receipt.", id)
		} else {
			writeLine(os.Stdout, "%s: DAMAGED. %s", id, strings.Join(res.Problems, "; "))
		}
	}
	if !res.OK {
		return &codedError{code: "verify_failed", err: fmt.Errorf("%s does not match its install receipt", id)}
	}
	return nil
}

func inferenceRemove(ctx context.Context, args []string) error {
	var yes, asJSON *bool
	id, _, path, err := parseModelArgs("remove", args, true, func(f *flag.FlagSet) {
		yes = f.Bool("yes", false, "confirm deleting the model files and receipt")
		asJSON = f.Bool("json", false, "print one JSON document")
	})
	if err != nil {
		return err
	}
	res, err := inference.Remove(inferenceDirOf(*path), id, *yes)
	if err != nil {
		return coded(err)
	}
	if *asJSON {
		return emit(res)
	}
	for _, r := range res.Removed {
		writeLine(os.Stdout, "removed %s", r)
	}
	return nil
}
