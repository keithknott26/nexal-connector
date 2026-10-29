package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/remoteinference"
)

// overflowCommand configures where this Mac's model server may send requests it cannot answer well: a
// serverless GPU and, separately, a cloud API. Nothing leaves this Mac until `overflow set --enabled=true` and
// the peers allowed to use each tier are listed. API keys are read from stdin into the Keychain and are never
// printed, put in config.json or passed on a command line.
func overflowCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nexal overflow status|set|set-key|clear-key  (see --help on each)")
	}
	action := args[0]
	f, path, err := flags("overflow " + action)
	if err != nil {
		return err
	}
	var (
		enabled                         = f.Bool("enabled", false, "turn overflow on or off")
		hard                            = f.String("hard-alias", "", "model name that asks for the GPU tier outright")
		maxWait                         = f.Int("max-wait-seconds", 0, "send to the GPU when the local queue would take longer than this")
		maxPrompt                       = f.Int("max-prompt-tokens", 0, "send to the GPU when the prompt is bigger than this")
		daily                           = f.Int("daily-limit", 0, "paid-tier requests per UTC day (0: no cap)")
		gpuName, gpuURL, gpuModel       = f.String("gpu-name", "gpu", "name shown to callers"), f.String("gpu-url", "", "https OpenAI-compatible base, e.g. https://api.runpod.ai/v2/<endpoint>/openai/v1"), f.String("gpu-model", "", "model id on the GPU endpoint")
		gpuFirst                        = f.Int("gpu-first-byte-seconds", 0, "seconds to wait for the GPU before trying the next tier (0: 90)")
		cloudName, cloudURL, cloudModel = f.String("cloud-name", "cloud", "name shown to callers"), f.String("cloud-url", "", "https OpenAI-compatible base"), f.String("cloud-model", "", "cloud model id")
		cloudFirst                      = f.Int("cloud-first-byte-seconds", 0, "seconds to wait for the cloud API (0: 90)")
		gpuPeers, cloudPeers            = f.String("gpu-peers", "", "comma-separated neXal IPs allowed to use the GPU tier, or *"), f.String("cloud-peers", "", "comma-separated neXal IPs allowed to use the cloud tier, or *")
		tierFlag                        = f.String("tier", "", "gpu or cloud (set-key and clear-key)")
		noGPU, noCloud                  = f.Bool("remove-gpu", false, "remove the GPU tier"), f.Bool("remove-cloud", false, "remove the cloud tier")
	)
	if err := parse(f, args[1:], path); err != nil {
		return err
	}
	set := map[string]bool{}
	f.Visit(func(x *flag.Flag) { set[x.Name] = true })

	unlock, err := config.Lock(*path)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	secrets, err := config.NewSecrets(*path, c)
	if err != nil {
		return err
	}
	switch action {
	case "status":
		return emit(overflowStatus(ctx, c, secrets))
	case "set-key", "clear-key":
		if *tierFlag != "gpu" && *tierFlag != "cloud" {
			return errors.New("usage: nexal overflow " + action + " --tier gpu|cloud   (set-key reads the key from stdin)")
		}
		name := "overflow-" + *tierFlag
		if action == "clear-key" {
			if err := secrets.Delete(ctx, name); err != nil {
				return err
			}
			return emit(overflowStatus(ctx, c, secrets))
		}
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 8192))
		if err != nil {
			return err
		}
		key := strings.TrimSpace(string(b))
		if len(key) < 16 || len(key) > 4096 || strings.ContainsAny(key, " \r\n\t") {
			return errors.New("the key must be 16-4096 characters with no spaces")
		}
		if err := secrets.Put(ctx, name, key); err != nil {
			return err
		}
		return emit(overflowStatus(ctx, c, secrets))
	case "set":
		o := config.OverflowConfig{}
		if c.Overflow != nil {
			o = *c.Overflow
		}
		if set["enabled"] {
			o.Enabled = *enabled
		}
		if set["hard-alias"] {
			o.HardAlias = *hard
		}
		if set["max-wait-seconds"] {
			o.MaxWaitSeconds = *maxWait
		}
		if set["max-prompt-tokens"] {
			o.MaxPromptTokens = *maxPrompt
		}
		if set["daily-limit"] {
			o.DailyLimit = *daily
		}
		tier := func(cur *config.OverflowTier, name, url, model *string, first *int, pre string) *config.OverflowTier {
			if !(set[pre+"-name"] || set[pre+"-url"] || set[pre+"-model"] || set[pre+"-first-byte-seconds"]) {
				return cur
			}
			t := config.OverflowTier{}
			if cur != nil {
				t = *cur
			}
			if set[pre+"-name"] {
				t.Name = *name
			}
			if set[pre+"-url"] {
				t.BaseURL = *url
			}
			if set[pre+"-model"] {
				t.Model = *model
			}
			if set[pre+"-first-byte-seconds"] {
				t.FirstByteSeconds = *first
			}
			if t.Name == "" {
				t.Name = pre
			}
			return &t
		}
		o.GPU = tier(o.GPU, gpuName, gpuURL, gpuModel, gpuFirst, "gpu")
		o.Cloud = tier(o.Cloud, cloudName, cloudURL, cloudModel, cloudFirst, "cloud")
		if *noGPU {
			o.GPU = nil
		}
		if *noCloud {
			o.Cloud = nil
		}
		if set["gpu-peers"] {
			o.GPUPeers = splitList(*gpuPeers)
		}
		if set["cloud-peers"] {
			o.CloudPeers = splitList(*cloudPeers)
		}
		c.Overflow = &o
		if err := c.Validate(); err != nil {
			return err
		}
		if err := config.Save(*path, c); err != nil {
			return err
		}
		st := overflowStatus(ctx, c, secrets)
		st["appliesAt"] = "the next time the model server starts; a model already being served keeps its settings"
		return emit(st)
	}
	return errors.New("unknown overflow action; use status, set, set-key or clear-key")
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// overflowStatus is what Settings shows. It reports whether a key is stored, never the key.
func overflowStatus(ctx context.Context, c config.Config, secrets config.Secrets) map[string]any {
	has := func(name string) bool { v, err := secrets.Get(ctx, name); return err == nil && v != "" }
	o := c.Overflow
	if o == nil {
		o = &config.OverflowConfig{}
	}
	return map[string]any{"overflow": o, "gpuKeyStored": has("overflow-gpu"), "cloudKeyStored": has("overflow-cloud"),
		"privacy": "when on, a listed peer's prompt can leave your network for the GPU or cloud provider; callers see x-nexal-backend on every reply"}
}

// buildOverflow turns the saved settings into the router the model server uses; nil means off. A tier whose
// key is missing is left out rather than sent unauthenticated.
func buildOverflow(ctx context.Context, c config.Config, secrets config.Secrets) *remoteinference.Overflow {
	o := c.Overflow
	if o == nil || !o.Enabled {
		return nil
	}
	tier := func(t *config.OverflowTier, secret string) *remoteinference.Tier {
		if t == nil {
			return nil
		}
		key, err := secrets.Get(ctx, secret)
		if err != nil || key == "" {
			return nil
		}
		return &remoteinference.Tier{Name: t.Name, BaseURL: t.BaseURL, APIKey: key, Model: t.Model,
			FirstByte: time.Duration(t.FirstByteSeconds) * time.Second}
	}
	set := func(l []string) map[string]bool {
		m := map[string]bool{}
		for _, p := range l {
			m[p] = true
		}
		return m
	}
	ov := &remoteinference.Overflow{Enabled: true, HardAlias: o.HardAlias, MaxWait: time.Duration(o.MaxWaitSeconds) * time.Second,
		MaxPrompt: o.MaxPromptTokens, DailyLimit: o.DailyLimit, GPUPeers: set(o.GPUPeers), CloudPeers: set(o.CloudPeers),
		GPU: tier(o.GPU, "overflow-gpu"), Cloud: tier(o.Cloud, "overflow-cloud")}
	if ov.GPU == nil && ov.Cloud == nil {
		return nil
	}
	return ov
}
