// Package anthropicapi implements ports.LanguageModel with the Anthropic API.
package anthropicapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"

	"github.com/arrno/diff-duck/internal/services/ports"
)

const maxTokens = 16000

// Model calls the Messages API. Credentials resolve the SDK's usual way.
type Model struct {
	client anthropic.Client
	model  string
	effort string
}

// New returns an API model.
func New(model, effort string) *Model { return newWithOptions(model, effort) }

func newWithOptions(model, effort string, opts ...option.RequestOption) *Model {
	return &Model{client: anthropic.NewClient(opts...), model: model, effort: effort}
}

// Describe identifies this configuration.
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
		return ports.ModelResponse{}, translate(err)
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

func translate(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("anthropic api status %d: %s", apiErr.StatusCode, apiErr.Error())
	}
	return fmt.Errorf("anthropic api: %w", err)
}
