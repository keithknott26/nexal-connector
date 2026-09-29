// nexal-validation-lab executes one enrolled validation lease using a separate
// lab credential. It never reads or changes the installed connector enrollment.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"nexal/connector/internal/client"
	"nexal/connector/internal/cybersecurity"
	"os"
	"time"
)

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: nexal-validation-lab PRIVATE_CONFIG_JSON")
	}
	info, err := os.Lstat(os.Args[1])
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("configuration must be a private regular file (0600)")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		return err
	}
	var cfg struct {
		Coordinator string `json:"coordinator"`
		HostID      string `json:"hostId"`
		Token       string `json:"token"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("invalid configuration")
	}
	if cfg.Token == "" || !client.ValidID(cfg.HostID) {
		return fmt.Errorf("invalid lab enrollment")
	}
	api, err := client.New(cfg.Coordinator, cfg.Token, false)
	if err != nil {
		return fmt.Errorf("invalid coordinator")
	}
	directory, err := os.MkdirTemp("", "nexal-validation-lab-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()
	if err = api.ValidationTick(ctx, cfg.HostID, cybersecurity.Canary{Directory: directory}); err != nil {
		return fmt.Errorf("validation did not complete; inspect coordinator result")
	}
	fmt.Println("Validation poll completed; inspect the coordinator for the evidence-backed outcome.")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
