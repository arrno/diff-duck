package goanalysis

import (
	"testing"

	"github.com/arrno/duck-diff/internal/domain"
)

func unit(t *testing.T, src, name string) domain.Unit {
	t.Helper()
	a, err := New().Analyze("p/f.go", []byte("package p\n"+src))
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	for _, u := range a.Units {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("no unit %q in %+v", name, a.Units)
	return domain.Unit{}
}

func TestHashIgnoresFormattingAndComments(t *testing.T) {
	a := unit(t, "func F(a int) int {\n\treturn a+1\n}", "F")
	b := unit(t, "// Doc.\nfunc F(a int) int {\n\n\t// Note.\n\treturn a + 1\n}", "F")
	if a.Hash != b.Hash {
		t.Fatal("formatting or comments changed the hash")
	}
}

// Each pair has the same identifiers and child count, and differs only in syntax that is not a
// child node. A shape-only hash would call these identical.
func TestHashSeparatesLookalikeSyntax(t *testing.T) {
	pairs := map[string][2]string{
		"slice bound":   {"func F(a []int, x int) []int { return a[x:] }", "func F(a []int, x int) []int { return a[:x] }"},
		"for clause":    {"func F(i int) { for i++; ; { } }", "func F(i int) { for ; ; i++ { } }"},
		"operator":      {"func F(a, b int) int { return a + b }", "func F(a, b int) int { return a - b }"},
		"assign token":  {"func F(a int) { a += 1 }", "func F(a int) { a -= 1 }"},
		"variadic call": {"func F(a []int) { g(a...) }", "func F(a []int) { g(a) }"},
		"chan dir":      {"func F(c chan<- int) {}", "func F(c <-chan int) {}"},
		"string value":  {"func F() string { return \"a\" }", "func F() string { return \"b\" }"},
		"elided type":   {"var F = []T{{a}}", "var F = []T{T{}}"},
	}
	for name, pair := range pairs {
		t.Run(name, func(t *testing.T) {
			if unit(t, pair[0], "F").Hash == unit(t, pair[1], "F").Hash {
				t.Fatalf("hash did not separate %q from %q", pair[0], pair[1])
			}
		})
	}
}

func TestTypeAliasDiffersFromDefinition(t *testing.T) {
	if unit(t, "type A = B", "A").Hash == unit(t, "type A B", "A").Hash {
		t.Fatal("alias and definition hashed the same")
	}
}

func TestStructHashExcludesFieldsWhichAreTheirOwnUnits(t *testing.T) {
	a := unit(t, "type S struct{ A int }", "S")
	b := unit(t, "type S struct{ A int; B string }", "S")
	if a.Hash != b.Hash {
		t.Fatal("adding a field registered as a direct struct change")
	}
	if unit(t, "type S[T any] struct{}", "S").Hash == unit(t, "type S struct{}", "S").Hash {
		t.Fatal("type parameters are part of the struct's own contract")
	}
}

func TestBodyHashOnlyProvesSubstantialBodies(t *testing.T) {
	if got := unit(t, "func F() error { return nil }", "F").BodyHash; got != "" {
		t.Fatal("a one-statement body must not be able to prove a rename")
	}
	a := unit(t, "func F(a int) int { b := a; return b }", "F")
	b := unit(t, "func G(a int) int { b := a; return b }", "G")
	if a.BodyHash == "" || a.BodyHash != b.BodyHash {
		t.Fatal("identical bodies under different names should match")
	}
	if a.Hash == b.Hash {
		t.Fatal("the full hash must include the name")
	}
}

func TestReceiverForms(t *testing.T) {
	for src, want := range map[string]string{
		"func (r R) M() {}":       "R.M",
		"func (r *R) M() {}":      "R.M",
		"func (r *R[T]) M() {}":   "R.M",
		"func (r R[K, V]) M() {}": "R.M",
	} {
		u := unit(t, src, want)
		if u.Parent != "R" || u.Kind != domain.KindMethod {
			t.Errorf("%s: got parent %q kind %s", src, u.Parent, u.Kind)
		}
	}
}

func TestIotaPositionIsPartOfTheValue(t *testing.T) {
	a := unit(t, "const (\n\tA = iota\n\tB\n)", "B")
	b := unit(t, "const (\n\tX = iota\n\tA\n\tB\n)", "B")
	if a.Hash == b.Hash {
		t.Fatal("B's value shifted, so its hash must change")
	}
	c := unit(t, "const (\n\tA = 5\n\tB\n)", "B")
	d := unit(t, "const (\n\tX = 9\n\tA = 5\n\tB\n)", "B")
	if c.Hash != d.Hash {
		t.Fatal("without iota, position does not affect the value")
	}
	e := unit(t, "const (\n\tA = 6\n\tB\n)", "B")
	if c.Hash == e.Hash {
		t.Fatal("B inherits A's expression, so changing it changes B")
	}
}

func TestGroupedAndMultiNameDeclarations(t *testing.T) {
	a, err := New().Analyze("p/f.go", []byte("package p\ntype S struct {\n\tA, B int\n\tEmbedded\n\t*other.Thing\n}\ntype I interface {\n\tM()\n\tio.Reader\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.UnitKind{
		"S": domain.KindStruct, "S.A": domain.KindField, "S.B": domain.KindField, "S.Embedded": domain.KindField, "S.Thing": domain.KindField,
		"I": domain.KindInterface, "I.M": domain.KindInterfaceMethod, "I.io.Reader": domain.KindField,
	}
	got := map[string]domain.UnitKind{}
	for _, u := range a.Units {
		got[u.Name] = u.Kind
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%s: got %q want %q (all: %v)", name, got[name], kind, got)
		}
	}
}

func TestSyntaxErrorIsReportedNotGuessed(t *testing.T) {
	if _, err := New().Analyze("p/f.go", []byte("package p\nfunc {")); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestGeneratedFileIsFlagged(t *testing.T) {
	a, err := New().Analyze("p/f.go", []byte("// Code generated by x. DO NOT EDIT.\n\npackage p\n"))
	if err != nil || !a.Generated {
		t.Fatalf("generated=%v err=%v", a.Generated, err)
	}
}
