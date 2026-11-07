// Package claudecli implements ports.LanguageModel by running `claude -p`.
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/arrno/diff-duck/internal/services/ports"
)

// Model calls the Claude Code CLI.
type Model struct {
	binary string
	model  string
	effort string
	dir    string
}

// Available reports whether the Claude Code binary is on PATH. An empty binary means "claude".
func Available(binary string) bool {
	_, err := exec.LookPath(orDefault(binary))
	return err == nil
}

func orDefault(binary string) string {
	if binary == "" {
		return "claude"
	}
	return binary
}

// New returns a model that runs in a scratch directory it creates. Call Close when finished.
func New(binary, model, effort string) (*Model, error) {
	binary = orDefault(binary)
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("the claude-cli adapter needs %q on PATH: %w", binary, err)
	}
	dir, err := os.MkdirTemp("", "diff-duck-claude-")
	if err != nil {
		return nil, err
	}
	return &Model{binary: binary, model: model, effort: effort, dir: dir}, nil
}

// Close removes the scratch directory.
func (m *Model) Close() { _ = os.RemoveAll(m.dir) }

// Describe identifies this configuration.
func (m *Model) Describe() string { return fmt.Sprintf("claude-cli/%s/%s", m.model, m.effort) }

// Args returns the command line for a request. Analyzed code is untrusted, so the CLI runs
// with no tools, MCP servers, skills, or settings, from an empty directory where it cannot
// load the analyzed repository's instructions.
func (m *Model) Args(req ports.ModelRequest) []string {
	return []string{
		"-p",
		"--model", m.model,
		"--effort", m.effort,
		"--tools", "",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--output-format", "json",
		"--system-prompt", req.System,
		"--json-schema", string(req.Schema),
	}
}

type result struct {
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	StopReason       string          `json:"stop_reason"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	Usage            struct {
		InputTokens         int64 `json:"input_tokens"`
		CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadTokens     int64 `json:"cache_read_input_tokens"`
		OutputTokens        int64 `json:"output_tokens"`
	} `json:"usage"`
}

// Complete runs one generation. The request's data goes over stdin, never onto the command line.
func (m *Model) Complete(ctx context.Context, req ports.ModelRequest) (ports.ModelResponse, error) {
	cmd := exec.CommandContext(ctx, m.binary, m.Args(req)...)
	cmd.Dir = m.dir
	cmd.Stdin = strings.NewReader(req.User)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	var r result
	parseErr := json.Unmarshal(stdout.Bytes(), &r)
	if runErr != nil && parseErr != nil {
		return ports.ModelResponse{}, fmt.Errorf("claude: %w: %s", runErr, bounded(stderr.String()+stdout.String()))
	}
	if parseErr != nil {
		return ports.ModelResponse{}, fmt.Errorf("claude returned unreadable output: %w", parseErr)
	}
	return interpret(r)
}

func interpret(r result) (ports.ModelResponse, error) {
	if r.StopReason == "refusal" {
		return ports.ModelResponse{}, ports.ErrModelRefused
	}
	if r.IsError {
		return ports.ModelResponse{}, fmt.Errorf("claude reported %s: %s", r.Subtype, bounded(r.Result))
	}
	if r.StopReason == "max_tokens" {
		return ports.ModelResponse{}, errors.New("claude output was cut off by the token limit")
	}
	if len(r.StructuredOutput) == 0 || string(r.StructuredOutput) == "null" {
		return ports.ModelResponse{}, errors.New("claude returned no structured output")
	}
	return ports.ModelResponse{
		JSON:         r.StructuredOutput,
		InputTokens:  r.Usage.InputTokens + r.Usage.CacheCreationTokens + r.Usage.CacheReadTokens,
		OutputTokens: r.Usage.OutputTokens,
		CostUSD:      r.TotalCostUSD,
	}, nil
}

func bounded(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return strings.ToValidUTF8(s[:400], "") + "…"
	}
	return s
}
