package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/runpod"
)

// newRunPod builds the RunPod client; tests replace it to point at a fake server.
var newRunPod = func(key string) *runpod.Client { return &runpod.Client{Key: key} }

const runPodKeySecret = "overflow-gpu"

type runPodOptions struct {
	preset       string
	model        string
	quantization string
	gpuTypes     []string
	maxWorkers   int
	idleSeconds  int
	recreate     bool
	forget       bool
}

// overflowRunPod is `nexal overflow provision|deprovision|check`: it creates, removes or verifies the serverless
// GPU endpoint in the owner's RunPod account. The configuration lock is not held across the network calls, only
// while the saved settings are read and written, so a slow RunPod answer cannot block other commands.
//
// Setting up never turns offloading on and never adds a peer: it only fills in the GPU tier. Sending a peer's
// prompt to a third party stays the owner's separate choice.
func overflowRunPod(ctx context.Context, action, path string, opt runPodOptions) error {
	read := func() (config.Config, config.Secrets, error) {
		c, err := config.Load(path)
		if err != nil {
			return c, nil, err
		}
		secrets, err := config.NewSecrets(path, c)
		return c, secrets, err
	}
	update := func(change func(*config.OverflowConfig)) (config.Config, config.Secrets, error) {
		unlock, err := lockStoppingAgent(ctx, path)
		if err != nil {
			return config.Config{}, nil, err
		}
		defer unlock()
		c, err := config.Load(path)
		if err != nil {
			return c, nil, err
		}
		o := config.OverflowConfig{}
		if c.Overflow != nil {
			o = *c.Overflow
		}
		change(&o)
		c.Overflow = &o
		if err := c.Validate(); err != nil {
			return c, nil, err
		}
		if err := config.Save(path, c); err != nil {
			return c, nil, err
		}
		secrets, err := config.NewSecrets(path, c)
		return c, secrets, err
	}

	c, secrets, err := read()
	if err != nil {
		return err
	}
	var existing *config.RunPodRecord
	if c.Overflow != nil && c.Overflow.RunPod != nil {
		r := *c.Overflow.RunPod
		existing = &r
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	key, _ := secrets.Get(ctx, runPodKeySecret)

	switch action {
	case "check":
		st := overflowStatus(ctx, c, secrets)
		if existing == nil {
			st["runpodEndpointExists"] = false
			return emit(st)
		}
		if key == "" {
			return errors.New("save your RunPod API key first")
		}
		ok, err := newRunPod(key).Exists(ctx, existing.EndpointID)
		if err != nil {
			return err
		}
		st["runpodEndpointExists"] = ok
		return emit(st)

	case "deprovision":
		if existing == nil {
			return emit(overflowStatus(ctx, c, secrets))
		}
		if !opt.forget {
			if key == "" {
				return errors.New("the RunPod API key is needed to remove the endpoint; delete it in RunPod's console and use --forget to drop it here")
			}
			if err := newRunPod(key).Remove(ctx, existing.EndpointID, existing.TemplateID); err != nil {
				return err
			}
		}
		c, secrets, err = update(func(o *config.OverflowConfig) {
			if o.GPU != nil && o.RunPod != nil && o.GPU.BaseURL == runpod.BaseURLFor(o.RunPod.EndpointID) {
				o.GPU = nil
			}
			o.RunPod = nil
			if o.GPU == nil && o.Cloud == nil {
				o.Enabled, o.Always = false, false
			}
		})
		if err != nil {
			return err
		}
		return emit(overflowStatus(ctx, c, secrets))
	}

	// provision
	if key == "" {
		return errors.New("save your RunPod API key first (Settings, AI Offload)")
	}
	spec, rec, err := runPodSpec(opt)
	if err != nil {
		return err
	}
	if hf, _ := secrets.Get(ctx, "overflow-hf"); hf != "" {
		spec.HFToken = hf // the worker needs it only for gated or private models; the presets are public
	}
	client := newRunPod(key)
	if existing != nil {
		if opt.recreate {
			if err := client.Remove(ctx, existing.EndpointID, existing.TemplateID); err != nil {
				return err
			}
		} else {
			ok, err := client.Exists(ctx, existing.EndpointID)
			if err != nil {
				return err
			}
			if ok {
				st := overflowStatus(ctx, c, secrets)
				st["created"] = false
				st["note"] = "the endpoint is already set up; use --recreate to replace it"
				return emit(st)
			}
		}
	}
	res, err := client.Provision(ctx, spec)
	if err != nil {
		return err
	}
	rec.EndpointID, rec.TemplateID, rec.Model = res.EndpointID, res.TemplateID, res.Model
	rec.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	c, secrets, err = update(func(o *config.OverflowConfig) {
		o.RunPod = &rec
		o.GPU = &config.OverflowTier{Name: "RunPod GPU", BaseURL: res.BaseURL, Model: res.Model, FirstByteSeconds: 180}
		if o.HardAlias == "" {
			o.HardAlias = "qwen-large"
		}
		// One-click means ready to use: turn offloading on for the peers on this neXal network, and offload when
		// the local queue is long or a prompt is large (under the endpoint's context). Anything the owner set
		// before is kept.
		o.Enabled = true
		if len(o.GPUPeers) == 0 {
			o.GPUPeers = []string{"*"}
		}
		if !o.Always && o.MaxWaitSeconds == 0 && o.MaxPromptTokens == 0 {
			o.MaxWaitSeconds, o.MaxPromptTokens = 20, 6000
		}
	})
	if err != nil {
		// Do not leave a billable endpoint behind that the app has no record of.
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		_ = client.Remove(cleanup, res.EndpointID, res.TemplateID)
		return fmt.Errorf("the endpoint was created but could not be saved, so it was removed again: %w", err)
	}
	st := overflowStatus(ctx, c, secrets)
	st["created"] = true
	st["note"] = "the GPU tier is configured and offloading is on for the peers on this neXal network (change who under Overflow). The first request after idle waits for the model to load"
	return emit(st)
}

// runPodSpec turns the flags into what to create and the record to keep.
func runPodSpec(opt runPodOptions) (runpod.Spec, config.RunPodRecord, error) {
	var s runpod.Spec
	rec := config.RunPodRecord{Preset: opt.preset, MaxWorkers: opt.maxWorkers}
	if opt.preset == "custom" {
		if opt.model == "" || len(opt.gpuTypes) == 0 {
			return s, rec, errors.New("a custom setup needs --model and --gpu-types")
		}
		s = runpod.Spec{Model: opt.model, Quantization: opt.quantization, GPUTypes: opt.gpuTypes}
	} else {
		p, ok := runpod.PresetByID(opt.preset)
		if !ok {
			return s, rec, errors.New("--preset must be budget, standard, light or custom")
		}
		s = runpod.Spec{Model: p.Model, Quantization: p.Quantization, GPUTypes: p.GPUTypes, MaxModelLen: p.MaxModelLen, DiskGB: p.DiskGB}
	}
	s.MaxWorkers, s.IdleSeconds = opt.maxWorkers, opt.idleSeconds
	return s, rec, nil
}
