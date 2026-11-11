package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/internal/services/ports"
)

// bigModule is a Python module far over the model's field bound: an import, then functions
// whose bodies make up nearly all of it, then one statement of the module's own.
func bigModule(importLine string) domain.FileAnalysis {
	lines := []string{importLine, ""}
	var units []domain.Unit
	for f := range 40 {
		start := len(lines) + 1
		lines = append(lines, fmt.Sprintf("def f%d(x):", f))
		for i := range 20 {
			lines = append(lines, fmt.Sprintf("    x = x + %d  # BODY of f%d, padded to make the file large ...........", i, f))
		}
		lines = append(lines, "    return x", "")
		u := pyUnit(domain.KindFunction, fmt.Sprintf("m.f%d", f), "m", start, start+21, fmt.Sprint(f))
		u.Signature = fmt.Sprintf("def f%d(x)", f)
		u.Source = strings.Join(lines[start-1:start+21], "\n")
		units = append(units, u)
	}
	lines = append(lines, "main()")
	module := pyUnit(domain.KindModule, "m", "", 1, len(lines), importLine)
	module.Source = strings.Join(lines, "\n") + "\n"
	return domain.FileAnalysis{Package: domain.PackageRef{Dir: "app", Name: "app"}, Units: append([]domain.Unit{module}, units...)}
}

// A module whose only change is an import is explained from its own code: its statements, with
// each member reduced to its signature. The members' bodies are not the change, so leaving them
// out is not a truncation, and the explanation keeps its confidence.
func TestModuleLeafIsExplainedFromItsOwnCode(t *testing.T) {
	base, head := bigModule("import os"), bigModule("import sys")
	if len(head.Units[0].Source) <= maxFieldChars {
		t.Fatalf("the module must exceed the field bound: %d", len(head.Units[0].Source))
	}
	roots, _, err := buildTree(t, context.Background(),
		map[string]domain.FileAnalysis{"app/m.py": base},
		map[string]domain.FileAnalysis{"app/m.py": head},
		domain.FileChange{Status: domain.FileModified, OldPath: "app/m.py", NewPath: "app/m.py", Hunks: []domain.Hunk{{Lines: []domain.DiffLine{
			{Kind: domain.LineRemoved, OldLine: 1, Text: "import os"},
			{Kind: domain.LineAdded, NewLine: 1, Text: "import sys"},
		}}}})
	if err != nil {
		t.Fatal(err)
	}
	module := findNode(roots, "m")
	if module == nil || len(module.Children) != 0 {
		t.Fatalf("the module is a leaf: %+v", module)
	}

	var sent struct {
		Items []leafItem `json:"items"`
	}
	m, _ := compress(t, func(req ports.ModelRequest) (string, error) {
		if err := json.Unmarshal([]byte(req.User), &sent); err != nil {
			return "", err
		}
		return summarizeAll(req)
	}, module)
	if len(m.requests) != 1 || len(sent.Items) != 1 {
		t.Fatalf("one leaf call: %d", len(m.requests))
	}
	item := sent.Items[0]
	if item.Truncated || strings.Contains(item.Source, "BODY") {
		t.Errorf("member bodies were sent (truncated=%v):\n%.300s", item.Truncated, item.Source)
	}
	for _, want := range []string{"import sys\n", "def f0(x) … (22 lines)\n", "def f39(x) … (22 lines)\n", "main()\n"} {
		if !strings.Contains(item.Source, want) {
			t.Errorf("own code lacks %q:\n%s", want, item.Source)
		}
	}
	if !strings.Contains(item.Diff, "+import sys") {
		t.Errorf("the change itself is sent: %q", item.Diff)
	}
	if module.Summary.Confidence != domain.ConfidenceHigh || module.Summary.Uncertainty != "" {
		t.Errorf("a one-line import change keeps its confidence: %+v", module.Summary)
	}
}

// ownSourceOf links units given as source lines and returns the container's own source. Each
// unit's code is its lines' text from col on its first line, unless given.
type spec struct {
	kind       domain.UnitKind
	name       string
	start, end int
	col        int // where the unit's code starts on its first line
	sig        string
	code       string // the unit's code when it ends before its last line does
}

func ownSourceOf(t *testing.T, text string, specs ...spec) string {
	t.Helper()
	lines := strings.SplitAfter(text, "\n")
	var units []*keyedUnit
	for _, s := range specs {
		code := strings.Join(lines[s.start-1:s.end], "")[s.col:]
		if s.code != "" {
			code = s.code
		}
		units = append(units, &keyedUnit{Unit: domain.Unit{Kind: s.kind, Name: s.name, StartLine: s.start, EndLine: s.end, Signature: s.sig, Source: code}})
	}
	// Units arrive in any order; the container is the first spec.
	shuffled := append([]*keyedUnit{}, units[1:]...)
	linkMembers(append(shuffled, units[0]))
	return ownSource(units[0])
}

// Units sharing one range, such as the names of "a = b = 1", are members beside each other,
// and a member on a line with its container's own code stays as it is.
func TestOwnSourceKeepsSharedLinesAndSiblings(t *testing.T) {
	text := "class C:  # own\n    a = b = 1\n    def f(self):\n        BODY\n        BODY\n    z = 2"
	got := ownSourceOf(t, text,
		spec{domain.KindClass, "C", 1, 6, 0, "class C", ""},
		spec{domain.KindField, "C.a", 2, 2, 4, "a = b = 1", ""},
		spec{domain.KindField, "C.b", 2, 2, 4, "a = b = 1", ""},
		spec{domain.KindMethod, "C.f", 3, 5, 4, "def f(self)", ""},
		spec{domain.KindField, "C.z", 6, 6, 4, "z = 2", ""})
	want := "class C:  # own\n    a = b = 1 … (1 line)\n    def f(self) … (3 lines)\n    z = 2 … (1 line)\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	// A method on the class's opening line shares it with the header.
	text = "class A { void f() {\n    BODY\n}\n}"
	if got := ownSourceOf(t, text,
		spec{domain.KindClass, "A", 1, 4, 0, "class A", ""},
		spec{domain.KindMethod, "A.f()", 1, 3, 10, "void f()", "void f() {\n    BODY\n}"}); got != text {
		t.Errorf("a member sharing the header's line stays: %q", got)
	}
}

// Module code on a member's line is never hidden behind the member's signature: an import
// before a function, or two functions on one line, keep the whole line.
func TestOwnSourceNeverHidesCodeSharingAMembersLine(t *testing.T) {
	text := "import x from 'y'; foo(); function a() {\n  BODY\n}\nfunction b() { return 1 } function c() { return 2 }\nfunction d() {\n  BODY\n}\n"
	got := ownSourceOf(t, text,
		spec{domain.KindModule, "m", 1, 7, 0, "module m", ""},
		spec{domain.KindFunction, "m.a", 1, 3, len("import x from 'y'; foo(); "), "function a()", ""},
		spec{domain.KindFunction, "m.b", 4, 4, 0, "function b()", "function b() { return 1 }"},
		spec{domain.KindFunction, "m.c", 4, 4, len("function b() { return 1 } "), "function c()", ""},
		spec{domain.KindFunction, "m.d", 5, 7, 0, "function d()", ""})
	want := "import x from 'y'; foo(); function a() {\n  BODY\n}\nfunction b() { return 1 } function c() { return 2 }\nfunction d() … (3 lines)\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	// A field before a method on one line is not hidden inside the method either.
	text = "class A {\n    int a; void m() {\n        BODY\n    }\n}"
	got = ownSourceOf(t, text,
		spec{domain.KindClass, "A", 1, 5, 0, "class A", ""},
		spec{domain.KindField, "A.a", 2, 2, 4, "int a", ""},
		spec{domain.KindMethod, "A.m()", 2, 4, 12, "void m()", ""})
	if !strings.Contains(got, "int a; void m() {") {
		t.Errorf("the field's code is kept:\n%s", got)
	}
}

func TestOwnSourceIsOnlyForContainers(t *testing.T) {
	text := "def f():\n    def g():\n        BODY\n    return g"
	if got := ownSourceOf(t, text,
		spec{domain.KindFunction, "f", 1, 4, 0, "def f()", ""},
		spec{domain.KindFunction, "f.g", 2, 3, 4, "def g()", ""}); got != text {
		t.Errorf("a function keeps nested code, which is its logic: %q", got)
	}
}

// A module holds everything in it, from its first line to its last, even a class with its very
// lines; a member without a signature is shown by name.
func TestOwnSourceOfAModuleReachesItsEdges(t *testing.T) {
	text := "class K:\n    BODY\n"
	if got := ownSourceOf(t, text,
		spec{domain.KindModule, "m", 1, 2, 0, "module m.py", ""},
		spec{domain.KindClass, "m.K", 1, 2, 0, "class K", ""}); got != "class K … (2 lines)\n" {
		t.Errorf("a class spanning the module: %q", got)
	}
	text = "def f():\n    BODY\nX = 1\ndef g():\n    BODY\n"
	if got := ownSourceOf(t, text,
		spec{domain.KindModule, "m", 1, 5, 0, "module m.py", ""},
		spec{domain.KindFunction, "m.f", 1, 2, 0, "def f()", ""},
		spec{domain.KindVar, "m.X", 3, 3, 0, "", ""},
		spec{domain.KindFunction, "m.g", 4, 5, 0, "def g()", ""}); got != "def f() … (2 lines)\nm.X … (1 line)\ndef g() … (2 lines)\n" {
		t.Errorf("members on the module's first and last lines: %q", got)
	}
}

// A removed container is shown from the base, so its own source comes from the base's units.
func TestRemovedModuleIsExplainedFromItsOwnCode(t *testing.T) {
	base := bigModule("import os")
	removed := domain.FileChange{Status: domain.FileDeleted, OldPath: "app/m.py", Hunks: []domain.Hunk{{Lines: []domain.DiffLine{{Kind: domain.LineRemoved, OldLine: 1, Text: "import os"}}}}}
	roots, _, err := buildTree(t, context.Background(), map[string]domain.FileAnalysis{"app/m.py": base}, map[string]domain.FileAnalysis{}, removed)
	if err != nil {
		t.Fatal(err)
	}
	module := findNode(roots, "m")
	if module == nil || module.Op != domain.OpRemove || strings.Contains(module.Source, "BODY") || !strings.Contains(module.Source, "def f39(x) … (22 lines)") {
		t.Errorf("the removed module's own code: %+v", module)
	}
}
