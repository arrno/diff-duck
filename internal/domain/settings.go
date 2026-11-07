package domain

import "errors"

// ErrUnknownProvider is returned when a model provider is not one DiffDuck offers.
var ErrUnknownProvider = errors.New("unknown model provider: choose claude-code, codex, anthropic-api, or openai-api")

// ErrUnknownReadingLevel is returned when a reading level is not one of the five levels.
var ErrUnknownReadingLevel = errors.New("unknown reading level: choose 2nd-grade, 6th-grade, high-school, college, or phd")

// ModelProvider names who writes a diff's summaries.
type ModelProvider string

const (
	// ProviderClaudeCode is the local, logged-in claude CLI.
	ProviderClaudeCode ModelProvider = "claude-code"
	// ProviderCodex is the local, logged-in codex CLI.
	ProviderCodex ModelProvider = "codex"
	// ProviderAnthropicAPI is the Anthropic API, reached with a saved API key.
	ProviderAnthropicAPI ModelProvider = "anthropic-api"
	// ProviderOpenAIAPI is the OpenAI API, reached with a saved API key.
	ProviderOpenAIAPI ModelProvider = "openai-api"
)

// ModelProviders lists every provider in the order they are offered.
func ModelProviders() []ModelProvider {
	return []ModelProvider{ProviderClaudeCode, ProviderCodex, ProviderAnthropicAPI, ProviderOpenAIAPI}
}

// ParseModelProvider returns the provider named by s, or ErrUnknownProvider.
func ParseModelProvider(s string) (ModelProvider, error) {
	p := ModelProvider(s)
	if !p.Valid() {
		return "", ErrUnknownProvider
	}
	return p, nil
}

// Valid reports whether the provider is one DiffDuck offers.
func (p ModelProvider) Valid() bool { return p.Label() != "" }

// Label is the provider's name as a person reads it. It is empty for an unknown provider.
func (p ModelProvider) Label() string {
	switch p {
	case ProviderClaudeCode:
		return "Claude Code"
	case ProviderCodex:
		return "Codex"
	case ProviderAnthropicAPI:
		return "Anthropic API"
	case ProviderOpenAIAPI:
		return "OpenAI API"
	}
	return ""
}

// NeedsKey reports whether the provider can be used only with a saved API key.
func (p ModelProvider) NeedsKey() bool {
	return p == ProviderAnthropicAPI || p == ProviderOpenAIAPI
}

// ReadingLevel is how plainly summaries are written.
type ReadingLevel string

const (
	ReadingSecondGrade ReadingLevel = "2nd-grade"
	ReadingSixthGrade  ReadingLevel = "6th-grade"
	ReadingHighSchool  ReadingLevel = "high-school"
	ReadingCollege     ReadingLevel = "college"
	ReadingPhD         ReadingLevel = "phd"
)

// ReadingLevels lists every reading level from plainest to most technical.
func ReadingLevels() []ReadingLevel {
	return []ReadingLevel{ReadingSecondGrade, ReadingSixthGrade, ReadingHighSchool, ReadingCollege, ReadingPhD}
}

// ParseReadingLevel returns the reading level named by s, or ErrUnknownReadingLevel.
func ParseReadingLevel(s string) (ReadingLevel, error) {
	l := ReadingLevel(s)
	if !l.Valid() {
		return "", ErrUnknownReadingLevel
	}
	return l, nil
}

// Valid reports whether the reading level is one of the five levels.
func (l ReadingLevel) Valid() bool { return l.Label() != "" }

// Label is the reading level as a person reads it. It is empty for an unknown level.
func (l ReadingLevel) Label() string {
	switch l {
	case ReadingSecondGrade:
		return "2nd grade"
	case ReadingSixthGrade:
		return "6th grade"
	case ReadingHighSchool:
		return "High school"
	case ReadingCollege:
		return "College"
	case ReadingPhD:
		return "PhD"
	}
	return ""
}

// ModelSettings are the app-wide choices applied to the next diff started.
type ModelSettings struct {
	Provider     ModelProvider `json:"provider"`
	ReadingLevel ReadingLevel  `json:"readingLevel"`
}

// DefaultModelSettings are the settings used before a person saves any.
func DefaultModelSettings() ModelSettings {
	return ModelSettings{Provider: ProviderClaudeCode, ReadingLevel: ReadingSixthGrade}
}

// ParseModelSettings returns settings for the named provider and reading level, rejecting
// unknown values.
func ParseModelSettings(provider, readingLevel string) (ModelSettings, error) {
	s := ModelSettings{Provider: ModelProvider(provider), ReadingLevel: ReadingLevel(readingLevel)}
	if err := s.Validate(); err != nil {
		return ModelSettings{}, err
	}
	return s, nil
}

// Validate rejects an unknown provider or reading level.
func (s ModelSettings) Validate() error {
	if !s.Provider.Valid() {
		return ErrUnknownProvider
	}
	if !s.ReadingLevel.Valid() {
		return ErrUnknownReadingLevel
	}
	return nil
}
