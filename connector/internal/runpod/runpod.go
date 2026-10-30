// Package runpod creates, checks and removes the serverless GPU endpoint that backs "offload to a GPU".
//
// It talks to RunPod's REST API (https://docs.runpod.io/api-reference) with the owner's API key, which comes
// from the Keychain and is only ever sent in the Authorization header: it is never logged and never part of an
// error message. Creating an endpoint is a billable action in the owner's RunPod account, so nothing here runs
// unless the owner asked for it; the endpoint is created with no always-on workers (it scales to zero and costs
// nothing while idle) and a small cap on concurrent workers.
package runpod

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultAPI is RunPod's REST API.
	DefaultAPI = "https://rest.runpod.io/v1"
	// WorkerImage is RunPod's OpenAI-compatible vLLM worker. Pinned to a release rather than "latest" so that an
	// endpoint keeps working until the owner recreates it.
	WorkerImage = "runpod/worker-v1-vllm:v2.30.1"
	// DefaultMinCUDA keeps workers off hosts whose driver is too old for the worker image.
	DefaultMinCUDA = "12.8"

	maxBody = 1 << 20
)

// Preset is a ready-made choice of model and GPU class.
type Preset struct {
	ID           string
	Title        string
	Model        string
	Quantization string
	GPUTypes     []string // RunPod gpuTypeIds, in the order they are tried
	MaxModelLen  int
	DiskGB       int
}

// Presets are the one-click choices. The Hugging Face ids are public (no token needed).
var Presets = []Preset{
	// Cheapest first: RunPod's 16 GB tier is its lowest serverless price (about $0.58/h while a worker runs, none
	// while idle), then the 24 GB tier (about $0.75/h). A 14B AWQ model (about 9 GB) fits both with an 8k context.
	{ID: "budget", Title: "Budget: Qwen3 14B (AWQ), cheapest GPUs (16 GB, then 24 GB)", Model: "Qwen/Qwen3-14B-AWQ", Quantization: "awq",
		GPUTypes: []string{"NVIDIA RTX A4000", "NVIDIA RTX A4500", "NVIDIA RTX 4000 Ada Generation", "NVIDIA RTX 2000 Ada Generation",
			"NVIDIA L4", "NVIDIA RTX A5000", "NVIDIA GeForce RTX 3090"},
		MaxModelLen: 8192, DiskGB: 40},
	{ID: "standard", Title: "Standard: Qwen3 32B (AWQ), 48 GB GPU", Model: "Qwen/Qwen3-32B-AWQ", Quantization: "awq",
		GPUTypes:    []string{"NVIDIA A40", "NVIDIA RTX A6000", "NVIDIA L40", "NVIDIA L40S", "NVIDIA RTX 6000 Ada Generation"},
		MaxModelLen: 16384, DiskGB: 80},
	{ID: "light", Title: "Light: Qwen3 14B (AWQ), 24 GB GPU", Model: "Qwen/Qwen3-14B-AWQ", Quantization: "awq",
		GPUTypes:    []string{"NVIDIA RTX A5000", "NVIDIA GeForce RTX 3090", "NVIDIA GeForce RTX 4090", "NVIDIA L4"},
		MaxModelLen: 16384, DiskGB: 60},
}

// PresetByID returns the named preset.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Spec is what to create. Zero values take the defaults noted on each field.
type Spec struct {
	Model        string   // Hugging Face repo id (required)
	Quantization string   // "", awq, gptq, ... passed to the worker as QUANTIZATION
	GPUTypes     []string // required: RunPod gpuTypeIds, in the order they are tried
	MaxModelLen  int      // 0: the worker's default
	DiskGB       int      // 0: 60
	MaxWorkers   int      // 0: 1. The cost cap: at most this many GPUs run at once.
	IdleSeconds  int      // 0: 30. How long a worker waits for another request before it is stopped.
	MinCUDA      string   // "": DefaultMinCUDA
	Image        string   // "": WorkerImage
}

// Result is what was created.
type Result struct {
	EndpointID string
	TemplateID string
	BaseURL    string // the OpenAI-compatible base: https://api.runpod.ai/v2/<id>/openai/v1
	Model      string
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
var gpuPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{1,63}$`)

// ValidID reports whether s looks like a RunPod resource id; it is used before an id goes into a URL path.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// ValidModel reports whether s looks like a Hugging Face "owner/name" repo id.
func ValidModel(s string) bool { return modelPattern.MatchString(s) }

// BaseURLFor is the OpenAI-compatible base URL of an endpoint.
func BaseURLFor(endpointID string) string {
	return "https://api.runpod.ai/v2/" + endpointID + "/openai/v1"
}

func (s Spec) validate() error {
	if !ValidModel(s.Model) {
		return errors.New("the model must be a Hugging Face id like owner/name")
	}
	if s.Quantization != "" && !regexp.MustCompile(`^[a-z0-9_]{2,24}$`).MatchString(s.Quantization) {
		return errors.New("invalid quantization")
	}
	if len(s.GPUTypes) == 0 || len(s.GPUTypes) > 12 {
		return errors.New("choose between 1 and 12 GPU types")
	}
	for _, g := range s.GPUTypes {
		if !gpuPattern.MatchString(g) {
			return fmt.Errorf("invalid GPU type %q", g)
		}
	}
	if s.MaxModelLen < 0 || s.MaxModelLen > 1<<20 {
		return errors.New("invalid maximum context length")
	}
	if s.DiskGB < 0 || s.DiskGB > 500 {
		return errors.New("disk must be 0-500 GB")
	}
	if s.MaxWorkers < 0 || s.MaxWorkers > 10 {
		return errors.New("max workers must be 0-10 (0 uses 1)")
	}
	if s.IdleSeconds < 0 || s.IdleSeconds > 3600 {
		return errors.New("idle seconds must be 0-3600")
	}
	if s.MinCUDA != "" && !regexp.MustCompile(`^1[0-9]\.[0-9]$`).MatchString(s.MinCUDA) {
		return errors.New("invalid minimum CUDA version")
	}
	if s.Image != "" && !regexp.MustCompile(`^[a-z0-9][a-z0-9./_-]*:[A-Za-z0-9._-]+$`).MatchString(s.Image) {
		return errors.New("invalid worker image")
	}
	return nil
}

// Client talks to the RunPod REST API.
type Client struct {
	Base string // "" means DefaultAPI (tests point it at a fake server)
	Key  string
	HTTP *http.Client
}

// Error is a failed RunPod call. Its text never contains the API key.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "RunPod rejected the API key (it needs read and write access to Serverless)"
	case http.StatusTooManyRequests:
		return "RunPod is rate limiting requests; try again in a minute"
	}
	if e.Message != "" {
		return fmt.Sprintf("RunPod returned %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("RunPod returned %d", e.Status)
}

// IsNotFound reports whether err is RunPod saying the resource does not exist.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return DefaultAPI
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// do sends one request. out, when not nil, receives the decoded JSON body.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if strings.TrimSpace(c.Key) == "" {
		return errors.New("save your RunPod API key first")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rd)
	if err != nil {
		return errors.New("could not build the RunPod request")
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		// url.Error text can carry the request URL but never the headers; keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("could not reach RunPod: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// A reply that echoes the key (it should not) must not carry it into an error shown to the owner.
		return &Error{Status: resp.StatusCode, Message: strings.ReplaceAll(errorMessage(raw), c.Key, "[redacted]")}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("RunPod sent a reply that could not be read")
		}
	}
	return nil
}

// errorMessage pulls a short human message out of a RunPod error body.
func errorMessage(raw []byte) string {
	var v struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(raw, &v) == nil {
		msg = v.Error
		if msg == "" {
			msg = v.Message
		}
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 300 {
		// Cut on a rune boundary so a multi-byte character is never split.
		cut := 300
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut]
	}
	return msg
}

type templateBody struct {
	Name            string            `json:"name"`
	ImageName       string            `json:"imageName"`
	IsServerless    bool              `json:"isServerless"`
	Category        string            `json:"category"`
	ContainerDiskGB int               `json:"containerDiskInGb"`
	Env             map[string]string `json:"env"`
}

type endpointBody struct {
	Name             string   `json:"name"`
	TemplateID       string   `json:"templateId"`
	ComputeType      string   `json:"computeType"`
	GPUTypeIDs       []string `json:"gpuTypeIds"`
	GPUCount         int      `json:"gpuCount"`
	WorkersMin       int      `json:"workersMin"`
	WorkersMax       int      `json:"workersMax"`
	IdleTimeout      int      `json:"idleTimeout"`
	ExecutionTimeout int      `json:"executionTimeoutMs"`
	Flashboot        bool     `json:"flashboot"`
	ScalerType       string   `json:"scalerType"`
	ScalerValue      int      `json:"scalerValue"`
	MinCUDAVersion   string   `json:"minCudaVersion,omitempty"`
}

type idReply struct {
	ID string `json:"id"`
}

func suffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b)
}

// Provision creates the worker template and the endpoint that uses it. It never starts a worker: with no
// always-on workers nothing is billed until the first request. If the endpoint cannot be created the template
// is deleted again, so a failed attempt leaves nothing behind.
func (c *Client) Provision(ctx context.Context, s Spec) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	if s.DiskGB == 0 {
		s.DiskGB = 60
	}
	if s.MaxWorkers == 0 {
		s.MaxWorkers = 1
	}
	if s.IdleSeconds == 0 {
		s.IdleSeconds = 30
	}
	if s.MinCUDA == "" {
		s.MinCUDA = DefaultMinCUDA
	}
	if s.Image == "" {
		s.Image = WorkerImage
	}
	env := map[string]string{"MODEL_NAME": s.Model}
	if s.Quantization != "" {
		env["QUANTIZATION"] = s.Quantization
	}
	if s.MaxModelLen > 0 {
		env["MAX_MODEL_LEN"] = fmt.Sprint(s.MaxModelLen)
	}
	name := "nexal-overflow-" + suffix()

	var tpl idReply
	if err := c.do(ctx, http.MethodPost, "/templates", templateBody{Name: name, ImageName: s.Image, IsServerless: true,
		Category: "NVIDIA", ContainerDiskGB: s.DiskGB, Env: env}, &tpl); err != nil {
		return Result{}, fmt.Errorf("creating the worker template: %w", err)
	}
	if !ValidID(tpl.ID) {
		return Result{}, errors.New("RunPod did not return a template id")
	}
	var ep idReply
	err := c.do(ctx, http.MethodPost, "/endpoints", endpointBody{Name: name, TemplateID: tpl.ID, ComputeType: "GPU",
		GPUTypeIDs: s.GPUTypes, GPUCount: 1, WorkersMin: 0, WorkersMax: s.MaxWorkers, IdleTimeout: s.IdleSeconds,
		ExecutionTimeout: 300000, Flashboot: true, ScalerType: "QUEUE_DELAY", ScalerValue: 4, MinCUDAVersion: s.MinCUDA}, &ep)
	if err == nil && !ValidID(ep.ID) {
		err = errors.New("RunPod did not return an endpoint id")
	}
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		_ = c.do(cleanup, http.MethodDelete, "/templates/"+url.PathEscape(tpl.ID), nil, nil)
		return Result{}, fmt.Errorf("creating the endpoint: %w", err)
	}
	return Result{EndpointID: ep.ID, TemplateID: tpl.ID, BaseURL: BaseURLFor(ep.ID), Model: s.Model}, nil
}

// Exists reports whether the endpoint is still in the owner's account.
func (c *Client) Exists(ctx context.Context, endpointID string) (bool, error) {
	if !ValidID(endpointID) {
		return false, errors.New("invalid endpoint id")
	}
	err := c.do(ctx, http.MethodGet, "/endpoints/"+url.PathEscape(endpointID), nil, nil)
	switch {
	case err == nil:
		return true, nil
	case IsNotFound(err):
		return false, nil
	}
	return false, err
}

// Remove deletes the endpoint and then its template. A resource that is already gone is not an error, so
// removing twice, or after the owner deleted it in RunPod's console, succeeds.
func (c *Client) Remove(ctx context.Context, endpointID, templateID string) error {
	if endpointID != "" {
		if !ValidID(endpointID) {
			return errors.New("invalid endpoint id")
		}
		if err := c.do(ctx, http.MethodDelete, "/endpoints/"+url.PathEscape(endpointID), nil, nil); err != nil && !IsNotFound(err) {
			return fmt.Errorf("removing the endpoint: %w", err)
		}
	}
	if templateID != "" {
		if !ValidID(templateID) {
			return errors.New("invalid template id")
		}
		if err := c.do(ctx, http.MethodDelete, "/templates/"+url.PathEscape(templateID), nil, nil); err != nil && !IsNotFound(err) {
			return fmt.Errorf("removing the worker template: %w", err)
		}
	}
	return nil
}
