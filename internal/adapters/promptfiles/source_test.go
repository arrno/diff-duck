package promptfiles

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/prompts"
)

func builtIn(t *testing.T) *Source {
	t.Helper()
	sub, err := fs.Sub(prompts.Default, "default")
	if err != nil {
		t.Fatal(err)
	}
	return FromFS(sub, "default")
}

func TestTheBuiltInVariantHasEveryReadingLevel(t *testing.T) {
	s := builtIn(t)
	hashes := map[string]domain.ReadingLevel{}
	for _, level := range domain.ReadingLevels() {
		text, info, err := s.LoadReadingLevel(level)
		if err != nil {
			t.Fatalf("%s: %v", level, err)
		}
		if !strings.Contains(text, "Reading level: "+level.Label()) {
			t.Errorf("%s: the instruction does not name its level", level)
		}
		// Every level keeps the grounding rule, whatever its vocabulary.
		if !strings.Contains(text, "invented facts") {
			t.Errorf("%s: the instruction drops the grounding rule", level)
		}
		if info.Name != "reading-levels/"+string(level) {
			t.Errorf("%s: name %q", level, info.Name)
		}
		if other, seen := hashes[info.Hash]; seen {
			t.Errorf("%s and %s have the same text", level, other)
		}
		hashes[info.Hash] = level
	}
}

func TestTheBasePromptsLeaveVocabularyToTheReadingLevel(t *testing.T) {
	s := builtIn(t)
	for _, name := range []string{"leaf", "parent", "outcomes"} {
		text, _, err := s.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		// "Avoid jargon" contradicts the higher levels, so it lives only in the level files.
		if strings.Contains(text, "Avoid jargon") || strings.Contains(text, "plain language") {
			t.Errorf("%s still sets the vocabulary itself", name)
		}
		if !strings.Contains(text, "Being simple never means inventing") {
			t.Errorf("%s lost its grounding rule", name)
		}
	}
}

func TestOnlyAValidReadingLevelSelectsAFile(t *testing.T) {
	files := fstest.MapFS{
		"leaf.md":                   {Data: []byte("leaf")},
		"reading-levels/phd.md":     {Data: []byte("phd")},
		"reading-levels/../leaf.md": {Data: []byte("never")},
	}
	s := FromFS(files, "v")
	for _, level := range []domain.ReadingLevel{"../leaf", "PhD", "", "phd/../../leaf"} {
		if _, _, err := s.LoadReadingLevel(level); !errors.Is(err, domain.ErrUnknownReadingLevel) {
			t.Errorf("%q: got %v", level, err)
		}
	}
	if text, _, err := s.LoadReadingLevel(domain.ReadingPhD); err != nil || text != "phd" {
		t.Fatalf("%q %v", text, err)
	}
	if _, _, err := s.LoadReadingLevel(domain.ReadingCollege); err == nil || !strings.Contains(err.Error(), `"v"`) {
		t.Fatalf("a variant missing a level must say which variant: %v", err)
	}
}
