package main

import (
	"context"
	"encoding/json"
	"errors"
	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
	"nexal/connector/internal/cybersecurity"
	"time"
)

type scanRoots []string

func (s *scanRoots) String() string     { return "explicit scan directories" }
func (s *scanRoots) Set(v string) error { *s = append(*s, v); return nil }
func securityCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("security requires status, configure, scan, baseline, or findings")
	}
	action := args[0]
	f, path, err := flags("security " + action)
	if err != nil {
		return err
	}
	var roots scanRoots
	f.Var(&roots, "root", "explicit absolute scan directory; repeat for multiple directories")
	engine := f.String("engine", "", "absolute pinned YARA-X executable path")
	enabled := f.Bool("enabled", true, "enable scheduled local scans")
	approve := f.Bool("approve", false, "explicitly trust current script style as a baseline")
	if err = parse(f, args[1:], path); err != nil {
		return err
	}
	scanner := cybersecurity.ScannerForConfig(*path)
	switch action {
	case "findings":
		findings, err := scanner.Findings()
		if err != nil {
			return err
		}
		return emit(boundedLocalFindings(findings))
	case "status":
	case "configure":
		err = scanner.Configure(*enabled, roots, *engine)
	case "scan":
		state, e := scanner.Status()
		if e != nil {
			return e
		}
		if !state.Enabled {
			return errors.New("enable explicitly configured scan roots first")
		}
		err = scanner.Scan(ctx, time.Now(), scannerReporter(*path), true)
	case "baseline":
		if !*approve {
			return errors.New("baseline requires --approve after reviewing the configured scripts")
		}
		err = scanner.ApproveBaseline(ctx)
	default:
		return errors.New("security requires status, configure, scan, baseline, or findings")
	}
	if err != nil {
		return err
	}
	state, err := scanner.Status()
	if err != nil {
		return err
	}
	return emit(state)
}
func runScanner(ctx context.Context, scanner cybersecurity.Scanner, report func(context.Context, cybersecurity.Event) error) {
	for {
		_ = scanner.Scan(ctx, time.Now(), report, false)
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Credentials are only accessed when an actual finding needs delivery. Failure
// retains the local outbox and does not prevent an offline scan.
func scannerReporter(path string) func(context.Context, cybersecurity.Event) error {
	var api *client.Client
	var host string
	return func(ctx context.Context, event cybersecurity.Event) error {
		if api == nil {
			c, err := config.Load(path)
			if err != nil {
				return errors.New("delivery unavailable")
			}
			if !client.ValidID(c.HostID) {
				return errors.New("delivery unavailable")
			}
			secrets, err := config.NewSecrets(path, c)
			if err != nil {
				return errors.New("delivery unavailable")
			}
			token, err := secrets.Get(ctx, "host")
			if err != nil {
				return errors.New("delivery unavailable")
			}
			api, err = client.New(c.Coordinator, token, c.Development)
			if err != nil {
				return errors.New("delivery unavailable")
			}
			host = c.HostID
		}
		return api.ReportSecurityEvent(ctx, host, event)
	}
}

type localFindingResponse struct {
	Findings  []cybersecurity.LocalFinding `json:"findings"`
	Truncated bool                         `json:"truncated"`
}

// The native helper transport caps stdout at64KiB. Preserve the full private
// ledger while returning the newest complete records that fit below that cap.
func boundedLocalFindings(findings []cybersecurity.LocalFinding) localFindingResponse {
	response := localFindingResponse{Findings: []cybersecurity.LocalFinding{}}
	for _, finding := range findings {
		response.Findings = append(response.Findings, finding)
		encoded, _ := json.Marshal(response)
		if len(encoded) > 60<<10 {
			response.Findings = response.Findings[:len(response.Findings)-1]
			response.Truncated = true
			break
		}
	}
	return response
}
