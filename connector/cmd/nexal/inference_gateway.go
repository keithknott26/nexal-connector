package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"nexal/connector/internal/client"
	"nexal/connector/internal/config"
)

// gatewayRenew is how often a served model renews its storage-host lease (the coordinator's lease is 10 minutes).
var gatewayRenew = 4 * time.Minute

// storageHostLease lets the neXal storage host reach this Mac's AI ports (TCP 8001-8099) while a model is served:
// it asks the coordinator to open them, renews the lease while serving, and closes them when stop is called.
// Best effort: a Mac that is not enrolled, or a coordinator that does not offer it, just serves without it.
func storageHostLease(ctx context.Context, configPath string, out io.Writer) (stop func()) {
	noop := func() {}
	c, err := config.Load(configPath)
	if err != nil || c.HostID == "" {
		return noop
	}
	secrets, err := config.NewSecrets(configPath, c)
	if err != nil {
		return noop
	}
	token, err := secrets.Get(ctx, "host")
	if err != nil {
		return noop
	}
	api, err := client.New(c.Coordinator, token, c.Development)
	if err != nil {
		return noop
	}
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		announced, warned := false, false
		for {
			cctx, ccancel := context.WithTimeout(lctx, 20*time.Second)
			st, err := api.InferenceGateway(cctx, c.HostID, true)
			ccancel()
			switch {
			case err != nil && lctx.Err() == nil && !warned:
				fmt.Fprintf(os.Stderr, "the neXal storage host cannot reach this model: %v\n", err)
				warned = true
			case err == nil && st.State == "applied" && !announced:
				writeLine(out, "The neXal storage host can reach this model while it runs (TCP 8001-8099)")
				announced = true
			}
			select {
			case <-lctx.Done():
				return
			case <-time.After(gatewayRenew):
			}
		}
	}()
	return func() {
		cancel()
		<-done
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		_, _ = api.InferenceGateway(cctx, c.HostID, false)
	}
}
