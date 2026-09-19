// nexal-mlx-job is a private one-shot local supervisor, not a job server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	bridge "nexal/runtimebridge"
)

const usage = `Usage: nexal-mlx-job --owner-policy /absolute/owner-policy.json \
  --admission /absolute/existing-admission.json --prompt-file /absolute/prompt.txt \
  --attempt-id EXISTING_ATTEMPT --model-manifest-sha256 REVIEWED_SHA256 \
  [--max-tokens 128] [--timeout 5m] [--stdout-bytes 1048576] [--stderr-bytes 65536]

Darwin arm64 only. Consumes existing owner-approved files and admission.
No downloads, admission creation, remote commands, listeners, or scheduling.
Standard output is one validated JSON result; errors never include child output.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("nexal-mlx-job", flag.ContinueOnError)
	// flag errors can echo arbitrary input. Suppress them and emit stable codes.
	flags.SetOutput(io.Discard)
	var ownerPath string
	var job bridge.LocalJob
	flags.StringVar(&ownerPath, "owner-policy", "", "existing private owner policy")
	flags.StringVar(&job.AdmissionPath, "admission", "", "existing local admission")
	flags.StringVar(&job.PromptPath, "prompt-file", "", "local prompt")
	flags.StringVar(&job.AttemptID, "attempt-id", "", "expected admission attempt")
	flags.StringVar(&job.ModelManifestSHA256, "model-manifest-sha256", "", "expected model digest")
	flags.IntVar(&job.MaxTokens, "max-tokens", 128, "maximum generated tokens")
	flags.DurationVar(&job.Timeout, "timeout", 5*time.Minute, "hard timeout, at most 5m")
	flags.IntVar(&job.StdoutBytes, "stdout-bytes", bridge.DefaultStdoutBytes, "stdout ceiling")
	flags.IntVar(&job.StderrBytes, "stderr-bytes", bridge.DefaultStderrBytes, "stderr ceiling")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(out, usage)
			return 0
		}
		return emitError(diagnostics, bridge.ErrInput)
	}
	if flags.NArg() != 0 || ownerPath == "" || job.AdmissionPath == "" || job.PromptPath == "" ||
		job.AttemptID == "" || job.ModelManifestSHA256 == "" {
		return emitError(diagnostics, bridge.ErrInput)
	}
	owner, err := bridge.LoadOwnerPolicy(ownerPath)
	if err != nil {
		return emitError(diagnostics, err)
	}
	result, err := bridge.RunLocalInference(ctx, owner, job)
	if err != nil {
		return emitError(diagnostics, err)
	}
	if json.NewEncoder(out).Encode(result) != nil {
		return emitError(diagnostics, bridge.ErrProcess)
	}
	return 0
}

func emitError(w io.Writer, err error) int {
	code, status := "LOCAL_INFERENCE_FAILED", 1
	switch {
	case errors.Is(err, context.Canceled):
		code, status = "CANCELED", 130
	case errors.Is(err, context.DeadlineExceeded):
		code, status = "DEADLINE_EXCEEDED", 124
	case errors.Is(err, bridge.ErrPlatform):
		code, status = "UNSUPPORTED_PLATFORM", 2
	case errors.Is(err, bridge.ErrPolicy):
		code, status = "OWNER_POLICY_REJECTED", 2
	case errors.Is(err, bridge.ErrInput):
		code, status = "INPUT_REJECTED", 2
	case errors.Is(err, bridge.ErrAdmission):
		code, status = "ADMISSION_REJECTED", 2
	case errors.Is(err, bridge.ErrOutputLimit):
		code = "OUTPUT_LIMIT_EXCEEDED"
	case errors.Is(err, bridge.ErrBusy):
		code = "LOCAL_RUNTIME_BUSY"
	case errors.Is(err, bridge.ErrResult):
		code = "RESULT_REJECTED"
	}
	// Never serialize err.Error(): neither OS errors nor adapter logs are public.
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
	return status
}
