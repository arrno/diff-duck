// Package openaiapi implements ports.LanguageModel with the OpenAI Responses API.
//
// It calls one endpoint, POST /v1/responses, with net/http instead of the official SDK, which
// alone added about 30 MB to the binary. The wire types below model only the fields diffduck
// sends and reads; transport.go owns HTTP, retries, and error translation.
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

	"github.com/arrno/diffduck/internal/services/ports"
)

// maxOutputTokens bounds one answer, reasoning included.
const maxOutputTokens = 32000

// Model calls the Responses API with an explicit API key.
type Model struct {
	api    *client
	model  string
	effort string
}

// New returns an API model that authenticates with apiKey. An empty effort leaves the
// model's default reasoning effort.
func New(apiKey, model, effort string) (*Model, error) {
	return newWithOptions(apiKey, model, effort)
}

func newWithOptions(apiKey, model, effort string, opts ...option) (*Model, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("the openai api adapter needs an API key")
	}
	if model == "" {
		return nil, errors.New("the openai api adapter needs a model ID")
	}
	return &Model{api: newClient(apiKey, opts...), model: model, effort: effort}, nil
}

// Describe identifies this configuration. It never includes the key.
func (m *Model) Describe() string { return fmt.Sprintf("openai-api/%s/%s", m.model, m.effort) }

// wireRequest is the subset of the Responses API request body diffduck sends.
type wireRequest struct {
	Model           string     `json:"model"`
	Instructions    string     `json:"instructions"`
	Input           string     `json:"input"`
	MaxOutputTokens int        `json:"max_output_tokens"`
	Store           bool       `json:"store"`
	Text            textConfig `json:"text"`
	Reasoning       *reasoning `json:"reasoning,omitempty"`
}

type textConfig struct {
	Format jsonSchemaFormat `json:"format"`
}

type jsonSchemaFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type reasoning struct {
	Effort string `json:"effort"`
}

// wireResponse is the subset of a Responses API response diffduck reads.
type wireResponse struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error  *apiError `json:"error"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// Complete runs one generation. It declares no tools, so analyzed code cannot trigger any,
// and asks OpenAI not to store the response.
func (m *Model) Complete(ctx context.Context, req ports.ModelRequest) (ports.ModelResponse, error) {
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(req.Schema, &schema); err != nil || schema == nil {
		return ports.ModelResponse{}, errors.New("openai api: the request schema must be a JSON object")
	}
	body := wireRequest{
		Model:           m.model,
		Instructions:    req.System,
		Input:           req.User,
		MaxOutputTokens: maxOutputTokens,
		Store:           false,
		Text: textConfig{Format: jsonSchemaFormat{
			Type: "json_schema", Name: "response", Schema: json.RawMessage(req.Schema), Strict: true,
		}},
	}
	if m.effort != "" {
		body.Reasoning = &reasoning{Effort: m.effort}
	}
	var resp wireResponse
	if err := m.api.createResponse(ctx, body, &resp); err != nil {
		return ports.ModelResponse{}, err
	}
	out, err := interpret(&resp)
	if err != nil && !errors.Is(err, ports.ErrModelRefused) {
		// Status, reason, and code come from the server; scrub them like any other reply text.
		return ports.ModelResponse{}, errors.New(m.api.redact(err.Error()))
	}
	return out, err
}

func interpret(resp *wireResponse) (ports.ModelResponse, error) {
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
	case "completed":
	case "incomplete":
		reason := ""
		if resp.IncompleteDetails != nil {
			reason = resp.IncompleteDetails.Reason
		}
		if reason == "content_filter" {
			return ports.ModelResponse{}, ports.ErrModelRefused
		}
		return ports.ModelResponse{}, fmt.Errorf("openai output was cut off (%s)", reason)
	case "failed":
		code := ""
		if resp.Error != nil {
			code = resp.Error.Code
		}
		return ports.ModelResponse{}, fmt.Errorf("openai response failed: %s", code)
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
