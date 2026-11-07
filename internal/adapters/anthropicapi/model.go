// Package anthropicapi implements ports.LanguageModel with the Anthropic API.
package anthropicapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"

	"github.com/arrno/diff-duck/internal/services/ports"
)

const maxTokens = 16000

// Model calls the Messages API.
type Model struct {
	client anthropic.Client
	key    string // the explicit key, kept only to redact it from errors
	model  string
	effort string
}

// New returns an API model whose credentials resolve the SDK's usual way (environment or
// profile).
func New(model, effort string) *Model { return newWithOptions(model, effort) }

// NewWithKey returns an API model that authenticates with apiKey and nothing else: no
// credential is read from the environment or a profile.
func NewWithKey(apiKey, model, effort string) (*Model, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("the anthropic api adapter needs an API key")
	}
	m := newWithOptions(model, effort, option.WithAPIKey(apiKey))
	m.key = apiKey
	return m, nil
}

func newWithOptions(model, effort string, opts ...option.RequestOption) *Model {
	return &Model{client: anthropic.NewClient(opts...), model: model, effort: effort}
}

// Describe identifies this configuration. It never includes a key.
func (m *Model) Describe() string { return fmt.Sprintf("anthropic-api/%s/%s", m.model, m.effort) }

// Complete runs one generation. It declares no tools, so analyzed code cannot trigger any.
func (m *Model) Complete(ctx context.Context, req ports.ModelRequest) (ports.ModelResponse, error) {
	resp, err := m.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     m.model,
		MaxTokens: maxTokens,
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		System:    []anthropic.BetaTextBlockParam{{Text: req.System}},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffort(m.effort),
			Format: anthropic.BetaJSONOutputFormatParam{Schema: req.Schema},
		},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(req.User)),
		},
	})
	if err != nil {
		return ports.ModelResponse{}, m.translate(ctx, err)
	}
	switch resp.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return ports.ModelResponse{}, ports.ErrModelRefused
	case anthropic.BetaStopReasonMaxTokens:
		return ports.ModelResponse{}, errors.New("output was cut off by the token limit")
	}
	var text string
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text += t.Text
		}
	}
	if !json.Valid([]byte(text)) {
		return ports.ModelResponse{}, errors.New("the model returned invalid JSON")
	}
	return ports.ModelResponse{
		JSON:         json.RawMessage(text),
		InputTokens:  resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}, nil
}

// translate builds a new error rather than wrapping the SDK's, with any explicit key
// redacted, so the key cannot reach a log or the browser (K1).
func (m *Model) translate(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		var body struct {
			Error struct{ Type, Message string } `json:"error"`
		}
		_ = json.Unmarshal([]byte(apiErr.RawJSON()), &body)
		detail := strings.TrimSpace(body.Error.Type + ": " + body.Error.Message)
		return fmt.Errorf("anthropic api status %d: %s", apiErr.StatusCode, redact(detail, m.key))
	}
	return fmt.Errorf("anthropic api: %s", redact(err.Error(), m.key))
}

// redact removes the key, then bounds the text, so a large error body cannot flood logs or
// stored diffs.
func redact(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "[redacted]")
	}
	if len(s) > 400 {
		s = strings.ToValidUTF8(s[:400], "") + "…"
	}
	return s
}
