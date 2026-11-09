// Package domain defines the compressed diff and the facts it is derived from.
package domain

import "strings"

// FileStatus is how Git classifies a changed file.
type FileStatus string

const (
	FileAdded    FileStatus = "added"
	FileDeleted  FileStatus = "deleted"
	FileModified FileStatus = "modified"
	FileRenamed  FileStatus = "renamed"
)

// LineKind classifies one line of a diff excerpt.
type LineKind string

const (
	LineContext LineKind = "context"
	LineAdded   LineKind = "added"
	LineRemoved LineKind = "removed"
	LineGap     LineKind = "gap"
)

// DiffLine is one line of a parsed diff. OldLine and NewLine are 0 when the line does not
// exist on that side.
type DiffLine struct {
	Kind    LineKind `json:"kind"`
	OldLine int      `json:"oldLine,omitempty"`
	NewLine int      `json:"newLine,omitempty"`
	Text    string   `json:"text,omitempty"`
}

// IsChange reports whether the line was added or removed.
func (l DiffLine) IsChange() bool { return l.Kind == LineAdded || l.Kind == LineRemoved }

// Hunk is a contiguous run of diff lines.
type Hunk struct {
	Lines []DiffLine
}

// FileChange is one file in the diff between two revisions.
type FileChange struct {
	Status  FileStatus
	OldPath string // empty for added files
	NewPath string // empty for deleted files
	Binary  bool
	Hunks   []Hunk
}

// Path returns the path that identifies the file: the new path unless it was deleted.
func (f FileChange) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// PackageRef identifies a language-level package.
type PackageRef struct {
	Dir  string
	Name string
}

// Key returns the package identity used to match it across revisions.
func (p PackageRef) Key() string { return p.Dir + "|" + p.Name }

// Unit is a code unit as it exists in one revision. All of its fields are facts from source
// analysis.
type Unit struct {
	Package   PackageRef
	Kind      UnitKind
	Name      string // qualified within the package, such as "Repo.Get"
	Parent    string // Name of the enclosing unit, or empty
	Signature string
	File      string
	StartLine int
	EndLine   int
	// Hash identifies the unit's code, ignoring formatting and comments.
	Hash string
	// BodyHash identifies a function's parameters, results, and body without its name or
	// receiver. Empty when the unit has no body, so it can never prove a rename.
	BodyHash string
	// ChangeNote explains a change that source text cannot show, from language knowledge.
	ChangeNote string
	Doc        string
	Source     string
	Test       bool
}

// FileAnalysis is the result of analyzing one file in one revision.
type FileAnalysis struct {
	Package   PackageRef
	Units     []Unit
	Generated bool
	// Test reports whether the file holds tests.
	Test bool
	// Constraints holds what decides whether the file is compiled at all. It lives outside
	// any code unit, so it needs its own comparison.
	Constraints string
}

// QualifiedName names a unit declared inside another, such as a method in a class or a class in
// a module: "util" and "Repo" give "util.Repo".
func QualifiedName(scope, name string) string { return scope + "." + name }

// ModuleSignature is the signature of the MODULE unit of the file at path: "module " and the
// path, such as "module pkg/util.py". It is the same in every language, so a module's
// signature tells a reader which file it is however the language names modules.
func ModuleSignature(path string) string { return "module " + path }

// Requalify gives a name qualified by one scope the same name in another, as when a module is
// renamed: Requalify("old.Repo.get", "old", "new") is "new.Repo.get". Other names are returned
// unchanged.
func Requalify(name, from, to string) string {
	switch {
	case from == "" || to == "":
		return name
	case name == from:
		return to
	case strings.HasPrefix(name, from+"."):
		return to + name[len(from):]
	}
	return name
}
