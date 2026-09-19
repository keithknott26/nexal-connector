package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nexal/experiments/tcp-pager/pager"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pager experiment stopped:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("commands: selftest | init | donor | client")
	}
	f := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	pages := f.Int("pages", 64, "donor pages, 2..256 (16 KiB each)")
	slots := f.Int("cache", 4, "resident page slots, 1..16")
	listen := f.String("listen", "127.0.0.1:0", "numeric private IP:port")
	addr := f.String("addr", "", "donor numeric private IP:port")
	keys := f.String("keys", "", "role-specific private credential directory")
	dir := f.String("dir", "", "new credential destination for init")
	helper := f.String("native-helper", "", "explicit native HVF helper; otherwise portable test")
	duration := f.Duration("timeout", 60*time.Second, "workload timeout, at most 2m")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *duration <= 0 || *duration > 2*time.Minute {
		return errors.New("timeout must be positive and at most 2m")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	emit := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	work := func(c *pager.Client) error {
		defer c.Close()
		wctx, cancel := context.WithTimeout(ctx, *duration)
		defer cancel()
		var r pager.Report
		var err error
		if *helper != "" {
			r, err = pager.Native(wctx, c, *slots, *helper)
		} else {
			r, err = pager.Portable(wctx, c, *slots)
		}
		if err != nil {
			return err
		}
		return emit(r)
	}
	switch os.Args[1] {
	case "init":
		if *dir == "" {
			return errors.New("init requires --dir (must not already exist)")
		}
		if err := pager.InitKeys(*dir); err != nil {
			return err
		}
		return emit(map[string]any{"created": *dir, "expiresInHours": 24, "warning": "private research keys; never commit or share the donor key"})
	case "donor":
		if *keys == "" {
			return errors.New("donor requires --keys")
		}
		if err := pager.PrivateAddress(*listen); err != nil {
			return err
		}
		s, err := pager.NewStore(*pages)
		if err != nil {
			return err
		}
		t, err := pager.LoadTLS(*keys, true)
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			return err
		}
		defer ln.Close()
		if err = emit(map[string]any{"listen": ln.Addr().String(), "reservedPayloadBytes": *pages * pager.PageSize, "volatile": true, "scope": "private experimental CPU pages only"}); err != nil {
			return err
		}
		return s.Serve(ctx, ln, t)
	case "client":
		if *keys == "" || *addr == "" {
			return errors.New("client requires --keys and --addr")
		}
		t, err := pager.LoadTLS(*keys, false)
		if err != nil {
			return err
		}
		c, err := pager.Dial(ctx, *addr, t)
		if err != nil {
			return err
		}
		return work(c)
	case "selftest":
		s, err := pager.NewStore(*pages)
		if err != nil {
			return err
		}
		k, err := pager.NewKeys()
		if err != nil {
			return err
		}
		st, err := k.ServerTLS()
		if err != nil {
			return err
		}
		ct, err := k.ClientTLS()
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		local, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.Serve(local, ln, st) }()
		defer func() { cancel(); <-done }()
		c, err := pager.Dial(ctx, ln.Addr().String(), ct)
		if err != nil {
			return err
		}
		return work(c)
	default:
		return errors.New("unknown command")
	}
}
