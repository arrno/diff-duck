package treesitter

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/arrno/diff-duck/internal/domain"
)

// extractor is one language's extraction step, run on a tree that already parsed cleanly.
type extractor func(context.Context, *Tree, string) (domain.FileAnalysis, error)

// languageCase is one tree-sitter language with a file shape that exercises it.
type languageCase struct {
	name    string
	grammar Grammar
	path    string
	extract extractor
}

var languages = []languageCase{
	{"python", pythonGrammar, "pkg/m.py", extractPython},
	{"typescript", typescriptGrammar, "pkg/m.ts", extractJS},
	{"rust", rustGrammar, "pkg/m.rs", extractRust},
	{"csharp", csharpGrammar, "pkg/M.cs", extractCSharp},
	{"java", javaGrammar, "pkg/M.java", extractJava},
}

// nested returns a file whose declarations nest depth levels deep, with a long string literal in
// the innermost one, so a unit at every level spans almost the whole file.
func nested(lang string, depth int, literal string) string {
	var b strings.Builder
	switch lang {
	case "python":
		for i := range depth {
			fmt.Fprintf(&b, "%sclass C:\n", strings.Repeat(" ", i))
		}
		fmt.Fprintf(&b, "%ss = %q\n", strings.Repeat(" ", depth), literal)
	case "typescript":
		// Namespaces qualify names but are not units; each level also holds a class.
		for range depth {
			b.WriteString("namespace N {\nexport class C { m() { return 1; } }\n")
		}
		fmt.Fprintf(&b, "export const s = %q;\n", literal)
		b.WriteString(strings.Repeat("}\n", depth))
	case "rust":
		// Inline modules flatten into the file's module; each level also holds a struct.
		for range depth {
			b.WriteString("mod m {\nstruct S { a: u8 }\n")
		}
		fmt.Fprintf(&b, "const S: &str = %q;\n", literal)
		b.WriteString(strings.Repeat("}\n", depth))
	case "csharp":
		b.WriteString(strings.Repeat("class C{", depth))
		fmt.Fprintf(&b, "string s = %q;", literal)
		b.WriteString(strings.Repeat("}", depth))
	case "java":
		b.WriteString(strings.Repeat("class C{", depth))
		fmt.Fprintf(&b, "String s = %q;", literal)
		b.WriteString(strings.Repeat("}", depth))
	}
	return b.String()
}

// flat returns a file of n top-level declarations.
func flat(lang string, n int) string {
	var b strings.Builder
	for i := range n {
		switch lang {
		case "python":
			fmt.Fprintf(&b, "def f%d():\n    return %d\n", i, i)
		case "typescript":
			fmt.Fprintf(&b, "function f%d() { return %d; }\n", i, i)
		case "rust":
			fmt.Fprintf(&b, "fn f%d() -> u32 { %d }\n", i, i)
		case "csharp":
			fmt.Fprintf(&b, "class C%d { int f() { return %d; } }\n", i, i)
		case "java":
			fmt.Fprintf(&b, "class C%d { int f() { return %d; } }\n", i, i)
		}
	}
	return b.String()
}

// members returns a file of one container holding n members.
func members(lang string, n int) string {
	var b strings.Builder
	open, member, closing := "", "", ""
	switch lang {
	case "python":
		open, member = "class C:\n", "    def f%d(self):\n        return %d\n"
	case "typescript":
		open, member, closing = "class C {\n", "f%d() { return %d; }\n", "}\n"
	case "rust":
		open, member, closing = "impl S {\n", "fn f%d() -> u32 { %d }\n", "}\n"
	case "csharp":
		open, member, closing = "enum E {\n", "A%d = %d,\n", "}\n"
	case "java":
		open, member, closing = "class C {\n", "int f%d() { return %d; }\n", "}\n"
	}
	b.WriteString(open)
	for i := range n {
		fmt.Fprintf(&b, member, i, i)
	}
	b.WriteString(closing)
	return b.String()
}

// Every unit's code is a slice of one copy of the file. Deeply nested declarations, each of
// which spans nearly the whole file, must not copy it once per level: 500 nested Python classes
// around a 1 MiB literal once allocated more than 500 MB.
func TestNestedDeclarationsShareOneCopyOfTheFile(t *testing.T) {
	rt := newTestRuntime(t)
	literal := strings.Repeat("x", 1<<20)
	for _, lc := range languages {
		t.Run(lc.name, func(t *testing.T) {
			src := nested(lc.name, 500, literal)
			var before, after goruntime.MemStats
			goruntime.GC()
			goruntime.ReadMemStats(&before)
			tree, err := rt.Parse(context.Background(), lc.grammar, []byte(src))
			if err != nil {
				t.Fatal(err)
			}
			a, err := lc.extract(context.Background(), tree, lc.path)
			goruntime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%d KB file allocated %d MB", len(src)>>10, (after.TotalAlloc-before.TotalAlloc)>>20)
			// Parsing alone takes tens of MB here; a copy per level would take hundreds.
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 160<<20 {
				t.Errorf("a %d KB file allocated %d MB", len(src)>>10, alloc>>20)
			}
			text := tree.Text()
			lo := uintptr(unsafe.Pointer(unsafe.StringData(text)))
			hi := lo + uintptr(len(text))
			spanning := 0
			for _, u := range a.Units {
				if len(u.Source) > len(literal) {
					spanning++
				}
				if u.Source == "" {
					t.Fatalf("%s %s has no code", u.Kind, u.Name)
				}
				p := uintptr(unsafe.Pointer(unsafe.StringData(u.Source)))
				if p < lo || p+uintptr(len(u.Source)) > hi {
					t.Fatalf("%s %s copies its code instead of sharing the file's", u.Kind, u.Name)
				}
			}
			if spanning == 0 {
				t.Fatalf("no unit spans the literal: %d units", len(a.Units))
			}
		})
	}
}

// nestedNames returns a file whose scopes nest depth levels deep, each named name, so every
// level's qualified name repeats all the names around it.
func nestedNames(lang string, depth int, name string) string {
	var b strings.Builder
	switch lang {
	case "python":
		for i := range depth {
			fmt.Fprintf(&b, "%sclass %s:\n", strings.Repeat(" ", i), name)
		}
		fmt.Fprintf(&b, "%spass\n", strings.Repeat(" ", depth))
	case "typescript":
		for range depth {
			fmt.Fprintf(&b, "namespace %s {\nexport class C {}\n", name)
		}
		b.WriteString(strings.Repeat("}\n", depth))
	case "rust":
		for range depth {
			fmt.Fprintf(&b, "mod %s {\nstruct S;\n", name)
		}
		b.WriteString(strings.Repeat("}\n", depth))
	case "csharp", "java":
		b.WriteString(strings.Repeat("class "+name+"{", depth))
		b.WriteString(strings.Repeat("}", depth))
	}
	return b.String()
}

// Qualified names repeat their enclosing scopes, so nesting long names makes them grow with the
// square of a file's size. Every language charges them to one budget linear in the file's size
// and refuses a file that outgrows it, rather than holding gigabytes of names.
func TestQualifiedNamesAreBoundedInEveryLanguage(t *testing.T) {
	rt := newTestRuntime(t)
	for _, lc := range languages {
		t.Run(lc.name, func(t *testing.T) {
			// About 450 KB; unbounded, its names would take about 67 MB.
			src := nestedNames(lc.name, 300, strings.Repeat("N", 1500))
			tree, err := rt.Parse(context.Background(), lc.grammar, []byte(src))
			if err != nil {
				t.Fatal(err)
			}
			var before, after goruntime.MemStats
			goruntime.GC()
			goruntime.ReadMemStats(&before)
			_, err = lc.extract(context.Background(), tree, lc.path)
			goruntime.ReadMemStats(&after)
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("names that grow far beyond the file are refused: %v", err)
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
				t.Errorf("refusing long names allocated %d MB", alloc>>20)
			}
			// The same nesting with short names is ordinary code.
			short := nestedNames(lc.name, 300, "N")
			tree, err = rt.Parse(context.Background(), lc.grammar, []byte(short))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lc.extract(context.Background(), tree, lc.path); err != nil {
				t.Errorf("short names nested as deeply are analyzed: %v", err)
			}
		})
	}
}

// A C# namespace's name repeats the namespaces around it, so nested namespaces are charged to
// the same budget, though they are not units.
func TestNestedCSharpNamespacesAreBounded(t *testing.T) {
	rt := newTestRuntime(t)
	name := strings.Repeat("N", 1500)
	src := strings.Repeat("namespace "+name+"{", 300) + "class C{}" + strings.Repeat("}", 300)
	tree, err := rt.Parse(context.Background(), csharpGrammar, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractCSharp(context.Background(), tree, "pkg/M.cs"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("namespace names that grow far beyond the file are refused: %v", err)
	}
}

// cancelAfter is a context that reports cancellation once Err has been asked a number of
// times, so a test can cancel extraction part way through without racing it.
type cancelAfter struct {
	context.Context
	allowed, calls int
}

func (c *cancelAfter) Err() error {
	c.calls++
	if c.calls > c.allowed {
		return context.Canceled
	}
	return nil
}

// A cancelled diff stops extraction promptly: the walk checks the context as it goes, rather
// than only during the WASM parse, and a partly extracted file is an error, not a short list.
func TestExtractionStopsWhenCancelled(t *testing.T) {
	rt := newTestRuntime(t)
	const declarations = 2000
	for _, lc := range languages {
		for shape, src := range map[string]string{
			"top-level declarations": flat(lc.name, declarations),
			"members of one type":    members(lc.name, declarations),
		} {
			t.Run(lc.name+"/"+shape, func(t *testing.T) { stopsWhenCancelled(t, rt, lc, src, declarations) })
		}
	}
}

func stopsWhenCancelled(t *testing.T, rt *Runtime, lc languageCase, src string, declarations int) {
	tree, err := rt.Parse(context.Background(), lc.grammar, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if a, err := lc.extract(context.Background(), tree, lc.path); err != nil || len(a.Units) < declarations {
		t.Fatalf("uncancelled: %d units, %v", len(a.Units), err)
	}
	ctx := &cancelAfter{Context: context.Background(), allowed: 10}
	a, err := lc.extract(ctx, tree, lc.path)
	if !errors.Is(err, context.Canceled) || len(a.Units) != 0 {
		t.Fatalf("want a cancellation error and no units, got %d units, %v", len(a.Units), err)
	}
	// Each declaration visited costs one check, so a walk that went on after the
	// cancellation would check thousands of times.
	if extra := ctx.calls - ctx.allowed; extra > 10 {
		t.Errorf("extraction went on for %d checks after it was cancelled", extra)
	}
}

// A MODULE unit's signature names its file by its repository path, in every language.
func TestModuleSignatureIsTheFilePathInEveryLanguage(t *testing.T) {
	rt := newTestRuntime(t)
	for _, lc := range languages {
		t.Run(lc.name, func(t *testing.T) {
			p := "src/app/" + lc.path
			tree, err := rt.Parse(context.Background(), lc.grammar, []byte(flat(lc.name, 1)))
			if err != nil {
				t.Fatal(err)
			}
			a, err := lc.extract(context.Background(), tree, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range a.Units {
				if u.Kind == domain.KindModule && u.Signature != "module "+p {
					t.Errorf("module signature %q, want %q", u.Signature, "module "+p)
				}
			}
		})
	}
}

func TestTextUntilAndNextSibling(t *testing.T) {
	tree, err := newTestRuntime(t).Parse(context.Background(), pythonGrammar, []byte("a = 1 # note\nb = 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	first := tree.Root().NamedChildren()[0]
	second := first.NextSibling()
	if second.Text() != "b = 2" {
		t.Fatalf("next sibling skips the comment: %q", second.Text())
	}
	if !second.NextSibling().IsNull() || !(Node{}).NextSibling().IsNull() {
		t.Error("the last sibling, and a null node, have no next sibling")
	}
	if got := first.TextUntil(second.EndByte()); got != "a = 1 # note\nb = 2" {
		t.Errorf("text to a later node's end: %q", got)
	}
	if second.TextUntil(first.StartByte()) != "" || first.TextUntil(len(tree.Text())+1) != "" || (Node{}).TextUntil(1) != "" {
		t.Error("a range that ends before its start or past the file is empty")
	}
	if got := second.TextUntil(len(tree.Text())); got != "b = 2\n" {
		t.Errorf("a range may end exactly at the end of the file: %q", got)
	}
}

func TestCompactMakesOneBoundedLine(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("ab ", 100)) // one line, single-spaced, 299 bytes
	for in, want := range map[string]string{
		"":                       "",
		"func f(a int) error":    "func f(a int) error",
		"func  f()":              "func f()",
		"func\tf()":              "func f()",
		"func f()\r\n{":          "func f() {",
		" func f()":              "func f()",
		"func f() ":              "func f()",
		"f(é)  x":                "f(é) x",
		"a b":                    "a b", // a non-ASCII space
		"a\rb":                   "a b",
		"a\nb":                   "a b",
		long:                     long[:240] + "…",
		strings.Repeat("é", 200): strings.ToValidUTF8(strings.Repeat("é", 200)[:240], "") + "…",
	} {
		if got := compact(in); got != want {
			t.Errorf("compact(%q) = %q, want %q", in, got, want)
		}
		if got := compact(compact(in)); got != compact(in) {
			t.Errorf("compact is not idempotent on %q: %q", in, got)
		}
	}
}

func TestLineCountCountsAFinalLineWithoutANewline(t *testing.T) {
	for in, want := range map[string]int{"": 1, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2} {
		if got := lineCount(in); got != want {
			t.Errorf("lineCount(%q) = %d, want %d", in, got, want)
		}
	}
}
