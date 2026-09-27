package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/cybersecurity"
)

// Explicit import of an existing capture: never starts packet capture, enables a
// honeypot, deletes evidence, or executes a remediation. Re-running with the same
// immutable file/source ID is idempotent, including after partial network failure.
func securityImportCommand(ctx context.Context, args []string) error {
	f, path, err := flags("security-import")
	if err != nil {
		return err
	}
	input := f.String("input", "", "absolute path to an immutable EVE JSONL capture")
	source := f.String("source-id", "", "persisted opaque capture ID")
	rules := f.String("rules-version", "", "public ruleset version")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if !filepath.IsAbs(*input) {
		return errors.New("--input must be an absolute path")
	}
	file, err := os.Open(*input)
	if err != nil {
		return errors.New("security capture unavailable")
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 16<<20 {
		return errors.New("capture must be a regular file of at most 16 MiB")
	}
	events, err := readSecurityCapture(file, *source, *rules, time.Now())
	if err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !client.ValidID(c.HostID) {
		return errors.New("enroll this host before importing security events")
	}
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return err
	}
	api, err := client.New(c.Coordinator, token, c.Development)
	if err != nil {
		return err
	}
	for i, e := range events {
		request, stop := context.WithTimeout(ctx, 15*time.Second)
		err := api.ReportSecurityEvent(request, c.HostID, e)
		stop()
		if err != nil {
			return fmt.Errorf("security import stopped after %d acknowledgements; retain the capture and retry with the same source ID: %w", i, err)
		}
	}
	return emit(map[string]any{"accepted": len(events), "sourceId": *source, "captureRetained": true})
}

func readSecurityCapture(file *os.File, source, rules string, now time.Time) ([]cybersecurity.Event, error) {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), cybersecurity.MaxEVEBytes+1)
	events := []cybersecurity.Event{}
	var line uint64
	// Bound parsing even if the input grows after Stat. Validate the entire batch
	// before transmitting anything, so malformed evidence cannot partially upload.
	var bytesRead int
	for scanner.Scan() {
		line++
		bytesRead += len(scanner.Bytes()) + 1
		if bytesRead > 16<<20 || line > 10000 {
			return nil, errors.New("capture input limit exceeded")
		}
		event, err := cybersecurity.NormalizeEVE(scanner.Bytes(), source, line, rules, now)
		if err != nil {
			return nil, fmt.Errorf("invalid security capture at line %d", line)
		}
		if event != nil {
			events = append(events, *event)
			if len(events) > 1000 {
				return nil, errors.New("capture exceeds 1000 alerts; split into separate captures with unique source IDs")
			}
		}
	}
	if scanner.Err() != nil {
		return nil, errors.New("security capture read failed or line exceeds limit")
	}
	if line == 0 {
		return nil, errors.New("capture is empty")
	}
	return events, nil
}
