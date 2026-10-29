package config

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// OverflowConfig lets the model server on this Mac send some requests to a serverless GPU (and, if the owner
// separately allows it, a cloud API) instead of the local model. Absent means off. Sending a peer's prompt
// to a third party is a privacy decision, so each tier is available only to the peers listed for it; API keys
// are never stored here (see the "overflow-gpu" and "overflow-cloud" secrets).
type OverflowConfig struct {
	Enabled         bool          `json:"enabled"`
	Always          bool          `json:"always,omitempty"`          // send every chat request to the GPU tier (still only for listed peers)
	HardAlias       string        `json:"hardAlias,omitempty"`       // a request naming this model asks for the GPU tier
	MaxWaitSeconds  int           `json:"maxWaitSeconds,omitempty"`  // go to the GPU when the local queue would take longer
	MaxPromptTokens int           `json:"maxPromptTokens,omitempty"` // ... or when the prompt is bigger than this
	DailyLimit      int           `json:"dailyLimit,omitempty"`      // paid-tier requests per UTC day; 0 = no cap
	GPU             *OverflowTier `json:"gpu,omitempty"`
	Cloud           *OverflowTier `json:"cloud,omitempty"`
	RunPod          *RunPodRecord `json:"runpod,omitempty"`     // the endpoint "set up" created in the owner's RunPod account
	GPUPeers        []string      `json:"gpuPeers,omitempty"`   // neXal IPs, or "*" for every peer
	CloudPeers      []string      `json:"cloudPeers,omitempty"` // a separate consent from GPUPeers
}

// RunPodRecord remembers what one-click setup created, so it can be checked and removed again. It holds ids
// only: the API key lives in the Keychain.
type RunPodRecord struct {
	EndpointID string `json:"endpointId"`
	TemplateID string `json:"templateId"`
	Preset     string `json:"preset,omitempty"`
	Model      string `json:"model"`
	MaxWorkers int    `json:"maxWorkers,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
}

func validResourceID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (r *RunPodRecord) validate() error {
	if r == nil {
		return nil
	}
	if !validResourceID(r.EndpointID) || (r.TemplateID != "" && !validResourceID(r.TemplateID)) {
		return errors.New("overflow runpod: invalid endpoint or template id")
	}
	if len(r.Model) > 200 || len(r.Preset) > 40 || r.MaxWorkers < 0 || r.MaxWorkers > 10 {
		return errors.New("overflow runpod: invalid record")
	}
	return nil
}

// OverflowTier is one OpenAI-compatible endpoint.
type OverflowTier struct {
	Name             string `json:"name"`
	BaseURL          string `json:"baseUrl"` // https, up to and including /v1
	Model            string `json:"model"`
	FirstByteSeconds int    `json:"firstByteSeconds,omitempty"` // wait this long for a reply before trying the next tier (0: 90)
}

func (t *OverflowTier) validate(what string) error {
	if t == nil {
		return nil
	}
	u, err := url.Parse(t.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New(what + ": baseUrl must be a plain URL without credentials or a query")
	}
	if u.Scheme != "https" {
		return errors.New(what + ": baseUrl must be https")
	}
	if strings.TrimSpace(t.Model) == "" || len(t.Model) > 200 {
		return errors.New(what + ": model is required")
	}
	if n := strings.TrimSpace(t.Name); n == "" || len(n) > 40 || strings.ContainsAny(n, "\r\n") {
		return errors.New(what + ": name is required (it is shown to callers)")
	}
	if t.FirstByteSeconds < 0 || t.FirstByteSeconds > 600 {
		return errors.New(what + ": firstByteSeconds must be 0-600")
	}
	return nil
}

func validOverflowPeers(what string, peers []string) error {
	for _, p := range peers {
		if p != "*" && net.ParseIP(p) == nil {
			return errors.New(what + ": " + p + " is not an IP address or *")
		}
	}
	return nil
}

func (o *OverflowConfig) Validate() error {
	if o == nil {
		return nil
	}
	if o.MaxWaitSeconds < 0 || o.MaxPromptTokens < 0 || o.DailyLimit < 0 {
		return errors.New("overflow: limits cannot be negative")
	}
	if len(o.HardAlias) > 100 || strings.ContainsAny(o.HardAlias, "\r\n") {
		return errors.New("overflow: invalid hardAlias")
	}
	if err := o.GPU.validate("overflow gpu"); err != nil {
		return err
	}
	if err := o.Cloud.validate("overflow cloud"); err != nil {
		return err
	}
	if err := o.RunPod.validate(); err != nil {
		return err
	}
	if err := validOverflowPeers("overflow gpuPeers", o.GPUPeers); err != nil {
		return err
	}
	if err := validOverflowPeers("overflow cloudPeers", o.CloudPeers); err != nil {
		return err
	}
	if o.Enabled && o.GPU == nil && o.Cloud == nil {
		return errors.New("overflow: enabled needs a gpu or cloud tier")
	}
	return nil
}
