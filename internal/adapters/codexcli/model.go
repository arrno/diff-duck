// Package codexcli implements ports.LanguageModel by running `codex exec` with the locally
// logged-in Codex CLI.
//
// Analyzed code is untrusted (K2). The request's data goes over stdin, never the command
// line. Codex runs from an empty scratch directory with the read-only sandbox, approvals
// never requested, user config, exec-policy rules and project instructions not loaded, web
// search off, the optional tool features it lets us switch off disabled, and no session
// files written. Our system prompt replaces Codex's agent instructions.
//
// Residual risk: unlike `claude --tools ""`, Codex cannot run with no tools at all. Some
// built-in tools (patch application, code-mode exec, sub-agent collaboration, user-input
// requests, MCP resource listing) stay declared. Code-mode exec fails closed because its
// host is disabled, writes are refused by the read-only sandbox, and the scratch directory
// is empty. A prompt injection in analyzed code can still make Codex start one such tool
// call, possibly one that reads files the local user can read. The adapter watches Codex's
// events as they arrive and kills Codex's process group at the first tool item, discarding
// the answer, so at most one tool call starts per request and nothing it produced is stored;
// that first call itself cannot be prevented. Users who need a hard guarantee should pick a
// provider without tools.
//
// Codex's JSON events carry no explicit refusal marker. Content-filter stops map to
// ports.ErrModelRefused; a refusal written as prose surfaces as an invalid-output error.
package codexcli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arrno/diff-duck/internal/services/ports"
)

const defaultBinary = "codex"

// disabledFeatures are the Codex features that add tools or load outside code. Codex fails
// on an unknown feature name, so the list holds only names Codex 0.165 knows; a Codex that
// drops one fails loudly instead of silently running with more capability.
var disabledFeatures = []string{
	"shell_tool", "unified_exec", "code_mode_host", "apps", "plugins", "multi_agent",
	"image_generation", "browser_use", "computer_use", "in_app_browser", "hooks",
	"view_image", "goals", "tool_suggest", "skill_search", "sleep_tool",
	"workspace_dependencies", "memories",
}

// Model calls the Codex CLI.
type Model struct {
	binary  string
	model   string
	effort  string
	root    string // holds the per-request instruction and schema files
	work    string // the empty directory Codex runs in
	timeout time.Duration
}

const (
	// callTimeout bounds one Codex run, so no input can keep Codex working indefinitely.
	callTimeout = 15 * time.Minute
	// waitDelay bounds how long a stopped Codex may hold its output open.
	waitDelay = 2 * time.Second
	// maxLine bounds one event line.
	maxLine = 64 << 20
)

// Available reports whether the Codex binary is on PATH. An empty binary means "codex".
func Available(binary string) bool {
	_, err := exec.LookPath(orDefault(binary))
	return err == nil
}

// New returns a model that runs in a scratch directory it creates. An empty model uses
// Codex's built-in default; an empty effort leaves Codex's default reasoning effort. Call
// Close when finished.
func New(binary, model, effort string) (*Model, error) {
	binary = orDefault(binary)
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("the codex adapter needs %q on PATH: %w", binary, err)
	}
	root, err := os.MkdirTemp("", "diff-duck-codex-")
	if err != nil {
		return nil, err
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return &Model{binary: binary, model: model, effort: effort, root: root, work: work, timeout: callTimeout}, nil
}

func orDefault(binary string) string {
	if binary == "" {
		return defaultBinary
	}
	return binary
}

// Close removes the scratch directory.
func (m *Model) Close() { _ = os.RemoveAll(m.root) }

// Describe identifies this configuration.
func (m *Model) Describe() string { return fmt.Sprintf("codex-cli/%s/%s", m.model, m.effort) }

// Args returns the command line for a request whose instructions and schema were written to
// instructionsFile and schemaFile. The prompt itself is read from stdin ("-").
func Args(model, effort, workDir, instructionsFile, schemaFile string) []string {
	args := []string{
		"exec",
		"--ignore-user-config",
		"--ignore-rules",
		"--skip-git-repo-check",
		"--ephemeral",
		"--sandbox", "read-only",
		"-c", "approval_policy=" + tomlString("never"),
		"-c", "web_search=" + tomlString("disabled"),
		"-c", "project_doc_max_bytes=0",
		"-c", "include_environment_context=false",
		"-c", "include_permissions_instructions=false",
		"-c", "include_apps_instructions=false",
		"-c", "include_collaboration_mode_instructions=false",
		"-c", "model_instructions_file=" + tomlString(instructionsFile),
	}
	for _, f := range disabledFeatures {
		args = append(args, "--disable", f)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+tomlString(effort))
	}
	return append(args,
		"--color", "never",
		"--json",
		"--output-schema", schemaFile,
		"--cd", workDir,
		"-",
	)
}

// tomlString quotes s as a TOML basic string. JSON string escapes are a subset of TOML's.
func tomlString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// Complete runs one generation. The request's data goes over stdin, never onto the command line.
// Codex's events are read as they arrive: the first tool call stops Codex at once, so analyzed
// code cannot keep it working, and so does cancelling ctx or running past the call timeout.
func (m *Model) Complete(ctx context.Context, req ports.ModelRequest) (ports.ModelResponse, error) {
	instructions, err := m.file("instructions", ".md", []byte(req.System))
	if err != nil {
		return ports.ModelResponse{}, err
	}
	schema, err := m.file("schema", ".json", req.Schema)
	if err != nil {
		return ports.ModelResponse{}, err
	}
	runCtx, stop := context.WithTimeout(ctx, m.timeout)
	defer stop()
	cmd := exec.CommandContext(runCtx, m.binary, Args(m.model, m.effort, m.work, instructions, schema)...)
	killProcessGroup(cmd)
	cmd.WaitDelay = waitDelay
	cmd.Dir = m.work
	cmd.Stdin = strings.NewReader(req.User)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ports.ModelResponse{}, err
	}
	if err := cmd.Start(); err != nil {
		return ports.ModelResponse{}, fmt.Errorf("codex: %w", err)
	}
	var s stream
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		if s.add(sc.Bytes()) {
			stop() // a tool was used: stop Codex before it does anything else
			break
		}
	}
	scanErr := sc.Err()
	if scanErr != nil {
		stop()
	}
	runErr := cmd.Wait()
	switch {
	case len(s.toolUse) > 0:
		return ports.ModelResponse{}, s.toolError()
	case ctx.Err() != nil:
		return ports.ModelResponse{}, ctx.Err()
	case runCtx.Err() != nil:
		return ports.ModelResponse{}, fmt.Errorf("codex did not finish within %s", m.timeout)
	case scanErr != nil:
		return ports.ModelResponse{}, fmt.Errorf("codex output was unreadable: %w", scanErr)
	}
	return s.result(runErr, stderr.String())
}

// file writes content to a file named by its hash, so concurrent requests share identical
// files and never see a partly written one.
func (m *Model) file(prefix, ext string, content []byte) (string, error) {
	sum := sha256.Sum256(content)
	path := filepath.Join(m.root, prefix+"-"+hex.EncodeToString(sum[:8])+ext)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	tmp, err := os.CreateTemp(m.root, prefix+"-*.tmp")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(content)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return path, nil
}

type event struct {
	Type  string `json:"type"`
	Item  *item  `json:"item"`
	Usage *struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Error   *struct{ Message string } `json:"error"`
	Message string                    `json:"message"`
}

type item struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Message string `json:"message"`
}

// harmlessItems are the item types that do not mean Codex used a tool. todo_list comes from
// Codex's always-on plan tool, which only records a plan and touches nothing.
var harmlessItems = map[string]bool{"agent_message": true, "reasoning": true, "error": true, "todo_list": true}

// stream accumulates the JSONL events of `codex exec --json`.
type stream struct {
	resp      ports.ModelResponse
	last      string
	completed bool
	failures  []string // the turn failed
	errs      []string // stream errors, fatal only when the turn did not complete
	toolUse   []string
}

// add records one event line and reports whether it shows Codex using a tool.
func (s *stream) add(line []byte) bool {
	line = bytes.TrimSpace(line)
	var e event
	if len(line) == 0 || json.Unmarshal(line, &e) != nil {
		return false
	}
	switch e.Type {
	case "turn.completed":
		s.completed = true
		if e.Usage != nil {
			s.resp.InputTokens += e.Usage.InputTokens
			s.resp.OutputTokens += e.Usage.OutputTokens
		}
	case "turn.failed":
		if e.Error != nil {
			s.failures = append(s.failures, e.Error.Message)
		} else {
			s.failures = append(s.failures, "turn failed")
		}
	case "error":
		s.errs = append(s.errs, e.Message)
	case "item.completed", "item.started", "item.updated":
		if e.Item == nil {
			return false
		}
		if !harmlessItems[e.Item.Type] {
			s.toolUse = append(s.toolUse, e.Item.Type)
			return true
		}
		if e.Type == "item.completed" && e.Item.Type == "agent_message" {
			s.last = e.Item.Text
		}
	}
	return false
}

func (s *stream) toolError() error {
	return fmt.Errorf("codex used a tool (%s) and was stopped; analyzed code may have prompted it", strings.Join(s.toolUse, ", "))
}

// result turns the recorded events into a response. Refusals become ports.ErrModelRefused;
// failures, truncation, missing or invalid output, and tool use become errors.
func (s *stream) result(runErr error, stderr string) (ports.ModelResponse, error) {
	failures := s.failures
	if !s.completed {
		failures = append(failures, s.errs...)
	}
	if len(s.toolUse) > 0 {
		return ports.ModelResponse{}, s.toolError()
	}
	if refused(failures) {
		return ports.ModelResponse{}, ports.ErrModelRefused
	}
	if len(failures) > 0 {
		if truncated(failures) {
			return ports.ModelResponse{}, fmt.Errorf("codex output was cut off: %s", bounded(strings.Join(failures, "; ")))
		}
		return ports.ModelResponse{}, fmt.Errorf("codex reported: %s", bounded(strings.Join(failures, "; ")))
	}
	if runErr != nil {
		return ports.ModelResponse{}, fmt.Errorf("codex: %w: %s", runErr, bounded(stderr))
	}
	if !s.completed {
		return ports.ModelResponse{}, errors.New("codex ended without completing its turn")
	}
	text := strings.TrimSpace(s.last)
	if text == "" {
		return ports.ModelResponse{}, errors.New("codex returned no output")
	}
	if !json.Valid([]byte(text)) {
		return ports.ModelResponse{}, fmt.Errorf("codex returned invalid JSON: %s", bounded(text))
	}
	resp := s.resp
	resp.JSON = json.RawMessage(text)
	return resp, nil
}

// interpret reads a complete event stream; tests use it to exercise result directly.
func interpret(stdout []byte, runErr error, stderr string) (ports.ModelResponse, error) {
	var s stream
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		if s.add(line) {
			break
		}
	}
	return s.result(runErr, stderr)
}

func refused(failures []string) bool {
	for _, f := range failures {
		l := strings.ToLower(f)
		if strings.Contains(l, "content_filter") || strings.Contains(l, "refusal") {
			return true
		}
	}
	return false
}

func truncated(failures []string) bool {
	for _, f := range failures {
		if strings.Contains(f, "max_output_tokens") || strings.Contains(strings.ToLower(f), "incomplete") {
			return true
		}
	}
	return false
}

func bounded(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return strings.ToValidUTF8(s[:400], "") + "…"
	}
	return s
}
