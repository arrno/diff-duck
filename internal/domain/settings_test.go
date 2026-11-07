package domain

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestUnknownValueErrorsNameEveryChoice(t *testing.T) {
	for _, p := range ModelProviders() {
		if !strings.Contains(ErrUnknownProvider.Error(), string(p)) {
			t.Errorf("ErrUnknownProvider does not offer %q", p)
		}
	}
	for _, l := range ReadingLevels() {
		if !strings.Contains(ErrUnknownReadingLevel.Error(), string(l)) {
			t.Errorf("ErrUnknownReadingLevel does not offer %q", l)
		}
	}
}

func TestParseModelProvider(t *testing.T) {
	cases := []struct {
		in       string
		want     ModelProvider
		label    string
		needsKey bool
		err      error
	}{
		{"claude-code", ProviderClaudeCode, "Claude Code", false, nil},
		{"codex", ProviderCodex, "Codex", false, nil},
		{"anthropic-api", ProviderAnthropicAPI, "Anthropic API", true, nil},
		{"openai-api", ProviderOpenAIAPI, "OpenAI API", true, nil},
		{"", "", "", false, ErrUnknownProvider},
		{"google", "", "", false, ErrUnknownProvider},
		{"Claude-Code", "", "", false, ErrUnknownProvider},
		{" codex", "", "", false, ErrUnknownProvider},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseModelProvider(c.in)
			if !errors.Is(err, c.err) || got != c.want {
				t.Fatalf("ParseModelProvider(%q) = %q, %v; want %q, %v", c.in, got, err, c.want, c.err)
			}
			p := ModelProvider(c.in)
			if p.Valid() != (c.err == nil) || p.Label() != c.label || p.NeedsKey() != c.needsKey {
				t.Fatalf("%q: valid=%v label=%q needsKey=%v", c.in, p.Valid(), p.Label(), p.NeedsKey())
			}
		})
	}
}

func TestModelProvidersListsEachProviderOnceInOrder(t *testing.T) {
	want := []ModelProvider{ProviderClaudeCode, ProviderCodex, ProviderAnthropicAPI, ProviderOpenAIAPI}
	if got := ModelProviders(); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	ModelProviders()[0] = "mutated"
	if ModelProviders()[0] != ProviderClaudeCode {
		t.Fatal("callers must not be able to change the provider list")
	}
}

func TestParseReadingLevel(t *testing.T) {
	cases := []struct {
		in    string
		want  ReadingLevel
		label string
		err   error
	}{
		{"2nd-grade", ReadingSecondGrade, "2nd grade", nil},
		{"6th-grade", ReadingSixthGrade, "6th grade", nil},
		{"high-school", ReadingHighSchool, "High school", nil},
		{"college", ReadingCollege, "College", nil},
		{"phd", ReadingPhD, "PhD", nil},
		{"", "", "", ErrUnknownReadingLevel},
		{"PhD", "", "", ErrUnknownReadingLevel},
		{"kindergarten", "", "", ErrUnknownReadingLevel},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseReadingLevel(c.in)
			if !errors.Is(err, c.err) || got != c.want {
				t.Fatalf("ParseReadingLevel(%q) = %q, %v; want %q, %v", c.in, got, err, c.want, c.err)
			}
			l := ReadingLevel(c.in)
			if l.Valid() != (c.err == nil) || l.Label() != c.label {
				t.Fatalf("%q: valid=%v label=%q", c.in, l.Valid(), l.Label())
			}
		})
	}
}

func TestReadingLevelsRunFromPlainestToMostTechnical(t *testing.T) {
	want := []ReadingLevel{ReadingSecondGrade, ReadingSixthGrade, ReadingHighSchool, ReadingCollege, ReadingPhD}
	if got := ReadingLevels(); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	ReadingLevels()[0] = "mutated"
	if ReadingLevels()[0] != ReadingSecondGrade {
		t.Fatal("callers must not be able to change the level list")
	}
}

func TestDefaultModelSettingsAreValid(t *testing.T) {
	d := DefaultModelSettings()
	if d.Provider != ProviderClaudeCode || d.ReadingLevel != ReadingSixthGrade {
		t.Fatalf("default: %+v", d)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("default must be valid: %v", err)
	}
}

func TestParseModelSettingsRejectsUnknownValues(t *testing.T) {
	cases := []struct {
		provider, level string
		err             error
	}{
		{"codex", "phd", nil},
		{"openai-api", "2nd-grade", nil},
		{"google", "phd", ErrUnknownProvider},
		{"codex", "kindergarten", ErrUnknownReadingLevel},
		{"", "", ErrUnknownProvider},
	}
	for _, c := range cases {
		got, err := ParseModelSettings(c.provider, c.level)
		if !errors.Is(err, c.err) {
			t.Fatalf("ParseModelSettings(%q, %q) error = %v; want %v", c.provider, c.level, err, c.err)
		}
		if c.err == nil && (string(got.Provider) != c.provider || string(got.ReadingLevel) != c.level) {
			t.Fatalf("ParseModelSettings(%q, %q) = %+v", c.provider, c.level, got)
		}
		if c.err != nil && got != (ModelSettings{}) {
			t.Fatalf("rejected settings must be empty: %+v", got)
		}
	}
	if err := (ModelSettings{Provider: ProviderCodex}).Validate(); !errors.Is(err, ErrUnknownReadingLevel) {
		t.Fatalf("a missing reading level must be rejected: %v", err)
	}
}

func TestGenerationRecordsSettingsAndDecodesOlderDiffs(t *testing.T) {
	var old Generation
	if err := json.Unmarshal([]byte(`{"model":"m","promptVariant":"v1"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Provider != "" || old.ReadingLevel != "" || old.Model != "m" {
		t.Fatalf("older diff: %+v", old)
	}
	if b, _ := json.Marshal(old); containsKey(b, "provider") || containsKey(b, "readingLevel") {
		t.Fatalf("empty settings must be omitted: %s", b)
	}

	b, err := json.Marshal(Generation{Provider: ProviderCodex, ReadingLevel: ReadingPhD})
	if err != nil {
		t.Fatal(err)
	}
	var back Generation
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Provider != ProviderCodex || back.ReadingLevel != ReadingPhD {
		t.Fatalf("round trip: %s -> %+v", b, back)
	}
	if !containsKey(b, "provider") || !containsKey(b, "readingLevel") {
		t.Fatalf("keys must be camelCase: %s", b)
	}
}

func TestModelSettingsUseCamelCaseKeys(t *testing.T) {
	b, err := json.Marshal(ModelSettings{Provider: ProviderOpenAIAPI, ReadingLevel: ReadingCollege})
	if err != nil {
		t.Fatal(err)
	}
	if !containsKey(b, "provider") || !containsKey(b, "readingLevel") {
		t.Fatalf("keys: %s", b)
	}
	var back ModelSettings
	if err := json.Unmarshal(b, &back); err != nil || back != (ModelSettings{Provider: ProviderOpenAIAPI, ReadingLevel: ReadingCollege}) {
		t.Fatalf("round trip: %s -> %+v, %v", b, back, err)
	}
}

func containsKey(b []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
