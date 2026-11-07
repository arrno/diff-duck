// Package openaiapi implements ports.LanguageModel with the OpenAI Responses API.
//
// Requests use strict structured outputs, so the request schema must follow OpenAI's strict
// subset: every object sets additionalProperties to false and lists all of its properties as
// required. The API rejects a schema that does not, and that rejection surfaces as an error.
package openaiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/arrno/diff-duck/internal/services/ports"
)

// maxOutputTokens bounds one answer, reasoning included.
const maxOutputTokens = 32000

// Model calls the Responses API with an explicit API key.
type Model struct {
	client openai.Client
	key    string
	model  string
	effort string
}

// New returns an API model that authenticates with apiKey. An empty effort leaves the
// model's default reasoning effort.
func New(apiKey, model, effort string) (*Model, error) {
	return newWithOptions(apiKey, model, effort)
}

func newWithOptions(apiKey, model, effort string, opts ...option.RequestOption) (*Model, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("the openai api adapter needs an API key")
	}
	if model == "" {
		return nil, errors.New("the openai api adapter needs a model ID")
	}
	opts = append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	return &Model{client: openai.NewClient(opts...), key: apiKey, model: model, effort: effort}, nil
}

// Describe identifies this configuration. It never includes the key.
func (m *Model) Describe() string { return fmt.Sprintf("openai-api/%s/%s", m.model, m.effort) }

// Complete runs one generation. It declares no tools, so analyzed code cannot trigger any,
// and asks OpenAI not to store the response.
func (m *Model) Complete(ctx context.Context, req ports.ModelRequest) (ports.ModelResponse, error) {
	var schema map[string]any
	if err := json.Unmarshal(req.Schema, &schema); err != nil || schema == nil {
		return ports.ModelResponse{}, errors.New("openai api: the request schema must be a JSON object")
	}
	params := responses.ResponseNewParams{
		Model:           m.model,
		Instructions:    openai.String(req.System),
		Input:           responses.ResponseNewParamsInputUnion{OfString: openai.String(req.User)},
		MaxOutputTokens: openai.Int(maxOutputTokens),
		Store:           openai.Bool(false),
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:   "response",
					Schema: schema,
					Strict: openai.Bool(true),
				},
			},
		},
	}
	if m.effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(m.effort)}
	}
	resp, err := m.client.Responses.New(ctx, params)
	if err != nil {
		return ports.ModelResponse{}, m.translate(ctx, err)
	}
	return interpret(resp)
}

func interpret(resp *responses.Response) (ports.ModelResponse, error) {
	var text strings.Builder
	for _, out := range resp.Output {
		if out.Type != "message" {
			continue
		}
		for _, c := range out.Content {
			switch c.Type {
			case "refusal":
				return ports.ModelResponse{}, ports.ErrModelRefused
			case "output_text":
				text.WriteString(c.Text)
			}
		}
	}
	switch resp.Status {
	case responses.ResponseStatusCompleted:
	case responses.ResponseStatusIncomplete:
		if resp.IncompleteDetails.Reason == "content_filter" {
			return ports.ModelResponse{}, ports.ErrModelRefused
		}
		return ports.ModelResponse{}, fmt.Errorf("openai output was cut off (%s)", resp.IncompleteDetails.Reason)
	case responses.ResponseStatusFailed:
		return ports.ModelResponse{}, fmt.Errorf("openai response failed: %s", resp.Error.Code)
	default:
		return ports.ModelResponse{}, fmt.Errorf("openai response ended with status %q", resp.Status)
	}
	out := strings.TrimSpace(text.String())
	if out == "" {
		return ports.ModelResponse{}, errors.New("openai returned no output")
	}
	if !json.Valid([]byte(out)) {
		return ports.ModelResponse{}, errors.New("openai returned invalid JSON")
	}
	return ports.ModelResponse{
		JSON:         json.RawMessage(out),
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}, nil
}

// translate builds a new error rather than wrapping the SDK's, so nothing that might echo
// the key can reach a log or the browser (K1).
func (m *Model) translate(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		detail := apiErr.Code
		if detail == "" {
			detail = apiErr.Type
		}
		// Authentication failures echo part of the key in their message, so keep only the code.
		if apiErr.StatusCode != 401 && apiErr.StatusCode != 403 && apiErr.Message != "" {
			detail += ": " + apiErr.Message
		}
		return fmt.Errorf("openai api status %d: %s", apiErr.StatusCode, redact(detail, m.key))
	}
	return fmt.Errorf("openai api: %s", redact(err.Error(), m.key))
}

func redact(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "[redacted]")
	}
	if len(s) > 400 {
		s = strings.ToValidUTF8(s[:400], "") + "…"
	}
	return s
}
