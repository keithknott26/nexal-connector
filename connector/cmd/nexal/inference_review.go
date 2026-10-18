package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"nexal/connector/internal/inference"
)

// Maintainer tools for reviewing a model snapshot before a release. They write nothing to the owner's own
// pins: the result goes into the reviewed-pins.json that ships with the app.
//
//	nexal inference review-latest <id> [--json]
//	nexal inference review <id> --revision <40-hex> --out <reviewed-pins.json> [--json]

var reviewRev = regexp.MustCompile(`^[0-9a-f]{40}$`)

// inferenceReviewLatest reads the commit a model's repository points at now, so a person can open it
// on Hugging Face and read it before approving.
func inferenceReviewLatest(ctx context.Context, args []string) error {
	var asJSON *bool
	id, _, path, err := parseModelArgs("review-latest", args, true, func(f *flag.FlagSet) {
		asJSON = f.Bool("json", false, "print one JSON document")
	})
	if err != nil {
		return err
	}
	base, err := inference.LoadCatalog()
	if err != nil {
		return err
	}
	repo := ""
	for _, e := range base.Entries {
		if e.ID == id {
			repo = e.HFRepo
		}
	}
	if repo == "" {
		return &codedError{code: "model_not_found", err: fmt.Errorf("%q is not a catalog model", id)}
	}
	client, err := inference.NewHFClient(inference.HFOptions{})
	if err != nil {
		return coded(err)
	}
	info, err := client.Info(ctx, repo, "main")
	if err != nil {
		return coded(err)
	}
	current := ""
	if c, cerr := inference.LoadCatalogWithPins(inferenceDirOf(*path)); cerr == nil {
		for _, m := range inference.ReviewedModels(c) {
			if m.ID == id {
				current = m.Revision
			}
		}
	}
	out := map[string]any{"modelId": id, "hfRepo": repo, "revision": info.SHA, "license": info.License, "gated": info.Gated,
		"commitUrl": "https://huggingface.co/" + repo + "/commit/" + info.SHA, "reviewedRevision": current, "upToDate": current == info.SHA}
	if *asJSON {
		return emit(out)
	}
	writeLine(os.Stdout, "%s: %s at %s (%s)", id, repo, info.SHA, info.License)
	writeLine(os.Stdout, "Read it: %s", out["commitUrl"])
	return nil
}

// inferenceReview hashes one exact commit and merges the pin into the reviewed-pins file.
func inferenceReview(ctx context.Context, args []string) error {
	var revision, out *string
	var asJSON *bool
	id, _, _, err := parseModelArgs("review", args, true, func(f *flag.FlagSet) {
		revision = f.String("revision", "", "the 40-hex commit you reviewed")
		out = f.String("out", "", "the reviewed-pins.json to update (connector/internal/inference/reviewed-pins.json)")
		asJSON = f.Bool("json", false, "print newline-delimited JSON events")
	})
	if err != nil {
		return err
	}
	if !reviewRev.MatchString(*revision) {
		return &codedError{code: "invalid_arguments", err: errors.New("--revision must be the full 40-hex commit")}
	}
	if filepath.Base(*out) != "reviewed-pins.json" {
		return &codedError{code: "invalid_arguments", err: errors.New("--out must be the reviewed-pins.json file")}
	}
	cur, err := readReviewed(*out)
	if err != nil {
		return &codedError{code: "invalid_arguments", err: err}
	}
	base, err := inference.LoadCatalog()
	if err != nil {
		return err
	}
	client, err := inference.NewHFClient(inference.HFOptions{})
	if err != nil {
		return coded(err)
	}
	tmp, err := os.MkdirTemp("", "nexal-review-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	enc := json.NewEncoder(os.Stdout)
	say := func(v map[string]any) {
		if *asJSON {
			_ = enc.Encode(v)
		}
	}
	var lastFile string
	var lastAt time.Time
	p := &inference.Pinner{InferenceDir: tmp, Catalog: base, Client: client, Progress: func(file string, n, total int64) {
		if file != lastFile || time.Since(lastAt) > 400*time.Millisecond || n == total {
			lastFile, lastAt = file, time.Now()
			say(map[string]any{"event": "progress", "file": file, "bytes": n, "total": total})
			if !*asJSON && n == total {
				writeLine(os.Stderr, "hashed %s", file)
			}
		}
	}}
	res, perr := p.Pin(ctx, id, *revision, "")
	if perr != nil {
		for _, b := range res.Blockers {
			say(map[string]any{"event": "blocker", "file": b.File, "key": b.Key, "detail": b.Detail})
		}
		e := inference.AsError(perr)
		say(map[string]any{"event": "error", "code": e.Code, "message": e.Message, "fix": e.Fix})
		return coded(perr)
	}
	pin := *res.PinJSON
	pin.PinnedAt = "" // keeps the shipped file stable between reviews
	cur.Pins[id] = pin
	data, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := inference.ParsePins(data); err != nil { // never write what we would refuse to read
		return err
	}
	tmpFile := *out + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, *out); err != nil {
		return err
	}
	say(map[string]any{"event": "done", "modelId": id, "revision": res.Revision, "files": len(res.Files), "totalBytes": res.TotalBytes, "license": res.License, "out": *out})
	if !*asJSON {
		writeLine(os.Stdout, "Added %s at %s (%d files, %s) to %s. Review the diff, then commit it.", id, res.Revision, len(res.Files), gibText(res.TotalBytes), *out)
	}
	return nil
}

func readReviewed(path string) (*inference.PinsFile, error) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s does not exist", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return inference.ParsePins(data)
}
