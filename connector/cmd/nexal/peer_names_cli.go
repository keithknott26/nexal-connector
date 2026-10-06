package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

// peerNamesCommand reads or sets the names people give peers of the secure network.
//
//	nexal peer-names --action list
//	    prints {"names":[{"address","name","updatedAt"}]}
//	nexal peer-names --action set --address 100.x.y.z
//	    reads the new name from stdin (one line; empty removes the name) and prints {"address","name"}
func peerNamesCommand(ctx context.Context, args []string) error {
	f, path, err := flags("peer-names")
	if err != nil {
		return err
	}
	action := f.String("action", "", "list | set")
	address := f.String("address", "", "the peer's mesh IPv4 (set)")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if err := checkPeerNameArgs(*action, *address); err != nil {
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
	ctx, stop := context.WithTimeout(ctx, 20*time.Second)
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
	return runPeerNames(ctx, api, c.HostID, *action, *address, os.Stdin, os.Stdout)
}

type peerNamesAPI interface {
	PeerNames(ctx context.Context, hostID string) (json.RawMessage, error)
	SetPeerName(ctx context.Context, hostID, address, name string) (json.RawMessage, error)
}

func checkPeerNameArgs(action, address string) error {
	switch action {
	case "list":
		if address != "" {
			return &codedError{code: "invalid_arguments", err: errors.New("peer-names list takes no --address")}
		}
	case "set":
		if !client.ValidPeerAddress(address) {
			return &codedError{code: "invalid_arguments", err: errors.New("--address must be a secure-network address (100.64.0.0/10)")}
		}
	default:
		return &codedError{code: "invalid_arguments", err: errors.New("usage: nexal peer-names --action list|set [--address 100.x.y.z]")}
	}
	return nil
}

func runPeerNames(ctx context.Context, api peerNamesAPI, hostID, action, address string, in io.Reader, out io.Writer) error {
	if err := checkPeerNameArgs(action, address); err != nil {
		return err
	}
	var body json.RawMessage
	var err error
	switch action {
	case "list":
		body, err = api.PeerNames(ctx, hostID)
	case "set":
		raw, rerr := io.ReadAll(io.LimitReader(in, 1024))
		if rerr != nil {
			return rerr
		}
		line, _, _ := strings.Cut(string(raw), "\n")
		name, cerr := client.CleanPeerName(line)
		if cerr != nil {
			return &codedError{code: "invalid_name", err: cerr}
		}
		body, err = api.SetPeerName(ctx, hostID, address, name)
	}
	if err != nil {
		return err
	}
	if len(body) == 0 {
		body = json.RawMessage("{}")
	}
	_, err = out.Write(append(body, '\n'))
	return err
}
