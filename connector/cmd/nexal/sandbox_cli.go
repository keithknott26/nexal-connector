package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

// sandboxAPI is the slice of the coordinator client the sandbox command uses.
type sandboxAPI interface {
	ListSandboxes(ctx context.Context, hostID string) (json.RawMessage, error)
	ConnectSandbox(ctx context.Context, hostID, id, kind, publicKey string) (json.RawMessage, error)
	CreateSandbox(ctx context.Context, hostID string, body json.RawMessage) (json.RawMessage, error)
	SandboxImages(ctx context.Context, hostID, runner string) (json.RawMessage, error)
}

// sandboxCommand is the Mac app's seam to throwaway hosts on the network:
//
//	nexal sandbox --action list
//	    prints {"sandboxes":[...]} exactly as GET /api/v2/hosts/:hostId/sandboxes returns it
//	nexal sandbox --action connect --id <id> --kind ssh|vnc|files [--public-key-stdin]
//	    prints the coordinator's connect response unchanged; for ssh and files the
//	    ssh-ed25519 public key to certify is read from stdin (one line)
//	nexal sandbox --action images [--id <runner host id>]
//	    prints the image catalog for that computer (default: this one)
//	nexal sandbox --action create
//	    reads the create request JSON object from stdin (imageId, runnerHostId, kind, ...)
//	    and prints the coordinator's 202 response unchanged
//
// On failure it exits non-zero with {"error":{"code","message"}} on stderr (the
// coordinator's error code is passed through, e.g. sandbox_not_running).
func sandboxCommand(ctx context.Context, args []string) error {
	f, path, err := flags("sandbox")
	if err != nil {
		return err
	}
	action := f.String("action", "", "list | connect | images | create")
	id := f.String("id", "", "sandbox id (connect)")
	kind := f.String("kind", "", "ssh | vnc | files (connect)")
	stdin := f.Bool("public-key-stdin", false, "read the ssh-ed25519 public key from standard input")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if err := checkSandboxArgs(*action, *id, *kind, *stdin); err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	ctx, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return err
	}
	api, err := client.New(c.Coordinator, token, c.Development)
	if err != nil {
		return err
	}
	if !client.ValidID(c.HostID) {
		return &codedError{code: "not_enrolled", err: errors.New("this computer is not enrolled; run enrollment first")}
	}
	return runSandbox(ctx, api, c.HostID, *action, *id, *kind, *stdin, os.Stdin, os.Stdout)
}

func checkSandboxArgs(action, id, kind string, stdin bool) error {
	switch action {
	case "list":
		if id != "" || kind != "" || stdin {
			return &codedError{code: "invalid_arguments", err: errors.New("sandbox list takes no --id, --kind or --public-key-stdin")}
		}
	case "images":
		if (id != "" && !client.ValidID(id)) || kind != "" || stdin {
			return &codedError{code: "invalid_arguments", err: errors.New("sandbox images takes only an optional --id <runner host id>")}
		}
	case "create":
		if id != "" || kind != "" || stdin {
			return &codedError{code: "invalid_arguments", err: errors.New("sandbox create reads its request from stdin and takes no other flags")}
		}
	case "connect":
		if !client.ValidID(id) {
			return &codedError{code: "invalid_arguments", err: errors.New("--id must be a sandbox id")}
		}
		switch kind {
		case "ssh", "files":
			if !stdin {
				return &codedError{code: "public_key_required", err: errors.New("ssh and files need --public-key-stdin")}
			}
		case "vnc":
			if stdin {
				return &codedError{code: "invalid_arguments", err: errors.New("vnc takes no public key")}
			}
		default:
			return &codedError{code: "invalid_arguments", err: errors.New("--kind must be ssh, vnc or files")}
		}
	default:
		return &codedError{code: "invalid_arguments", err: errors.New("usage: nexal sandbox --action list|images|create|connect [--id <id>] [--kind ssh|vnc|files] [--public-key-stdin]")}
	}
	return nil
}

func runSandbox(ctx context.Context, api sandboxAPI, hostID, action, id, kind string, readKey bool, in io.Reader, out io.Writer) error {
	if err := checkSandboxArgs(action, id, kind, readKey); err != nil {
		return err
	}
	var body json.RawMessage
	var err error
	switch action {
	case "list":
		body, err = api.ListSandboxes(ctx, hostID)
	case "images":
		body, err = api.SandboxImages(ctx, hostID, id)
	case "create":
		raw, rerr := io.ReadAll(io.LimitReader(in, 16<<10+1))
		trimmed := strings.TrimSpace(string(raw))
		if rerr != nil || len(raw) > 16<<10 || !strings.HasPrefix(trimmed, "{") || !json.Valid([]byte(trimmed)) {
			return &codedError{code: "invalid_request", err: errors.New("expected one JSON object (at most 16 KiB) on stdin")}
		}
		body, err = api.CreateSandbox(ctx, hostID, json.RawMessage(trimmed))
	default:
		key := ""
		if readKey {
			if key, err = readPublicKey(in); err != nil {
				return &codedError{code: "invalid_public_key", err: err}
			}
		}
		body, err = api.ConnectSandbox(ctx, hostID, id, kind, key)
	}
	if err != nil {
		return sandboxError(err)
	}
	if !json.Valid(body) {
		return errors.New("invalid coordinator response schema")
	}
	_, err = fmt.Fprintf(out, "%s\n", body)
	return err
}

// readPublicKey reads one ssh-ed25519 public key line (comment allowed) from r.
func readPublicKey(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 8<<10+1))
	if err != nil || len(b) > 8<<10 {
		return "", errors.New("public key unreadable or too large")
	}
	k := strings.TrimSpace(string(b))
	if !strings.HasPrefix(k, "ssh-ed25519 ") || strings.ContainsAny(k, "\r\n\x00") {
		return "", errors.New("expected a single-line ssh-ed25519 public key on stdin")
	}
	return k, nil
}

func sandboxError(err error) error {
	var status *client.StatusError
	if errors.As(err, &status) && status.Code != "" {
		return &codedError{code: status.Code, err: err}
	}
	if errors.As(err, &status) {
		switch status.Status {
		case 404:
			return &codedError{code: "sandbox_not_found", err: err}
		case 429:
			return &codedError{code: "rate_limited", err: err}
		}
	}
	if client.IsNotSupported(err) {
		return &codedError{code: "sandboxes_unavailable", err: errors.New("the coordinator does not offer throwaway hosts right now")}
	}
	return err
}
