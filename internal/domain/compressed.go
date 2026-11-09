package domain

import "time"

// ChangeOp is the operation applied to a code unit.
type ChangeOp string

const (
	OpAdd    ChangeOp = "ADD"
	OpRemove ChangeOp = "REMOVE"
	OpChange ChangeOp = "CHANGE"
)

// Valid reports whether the operation is one of the three defined operations.
func (o ChangeOp) Valid() bool { return o == OpAdd || o == OpRemove || o == OpChange }

// UnitKind is the structural kind of a node. It is language independent.
type UnitKind string

const (
	KindPackage         UnitKind = "PACKAGE"
	KindStruct          UnitKind = "STRUCT"
	KindInterface       UnitKind = "INTERFACE"
	KindType            UnitKind = "TYPE"
	KindFunction        UnitKind = "FUNCTION"
	KindMethod          UnitKind = "METHOD"
	KindInterfaceMethod UnitKind = "INTERFACE_METHOD"
	KindField           UnitKind = "FIELD"
	KindConst           UnitKind = "CONST"
	KindVar             UnitKind = "VAR"
	KindFile            UnitKind = "FILE"
	KindRegion          UnitKind = "REGION"

	// Kinds for languages other than Go.
	//
	// A module is one source file that is its own namespace, such as a Python, JavaScript, or
	// Rust file. It sits between a package and the units declared in it, and analyzers that
	// produce one must follow these rules, which StructuralDiffer relies on:
	//   - A file has at most one MODULE unit, and it spans the whole file.
	//   - Every other unit in the file is named QualifiedName(module, rest), and its Parent
	//     chain ends at the module.
	//   - Its Signature is ModuleSignature(file): "module " and the file's repository path,
	//     such as "module web/shop/cart.ts", in every language.
	KindModule     UnitKind = "MODULE"
	KindClass      UnitKind = "CLASS"
	KindEnum       UnitKind = "ENUM"
	KindEnumMember UnitKind = "ENUM_MEMBER"
	KindTrait      UnitKind = "TRAIT"
	KindRecord     UnitKind = "RECORD"
	KindProperty   UnitKind = "PROPERTY"
)

// Confidence states how far a generated explanation can be relied on.
type Confidence string

const (
	ConfidenceHigh Confidence = "high"
	ConfidenceLow  Confidence = "low"
)

// SummaryOrigin records where a node's explanation came from.
type SummaryOrigin string

const (
	// OriginNone means no explanation exists, and the viewer must say so.
	OriginNone SummaryOrigin = "none"
	// OriginStructural means the explanation is derived from source analysis.
	OriginStructural SummaryOrigin = "structural"
	// OriginModel means a language model generated the explanation.
	OriginModel SummaryOrigin = "model"
)

// Summary is the semantic layer of a node. It explains the node and never replaces its
// structural identity.
type Summary struct {
	SemanticName string        `json:"semanticName,omitempty"`
	Text         string        `json:"text,omitempty"`
	Confidence   Confidence    `json:"confidence,omitempty"`
	Uncertainty  string        `json:"uncertainty,omitempty"`
	Origin       SummaryOrigin `json:"origin"`
}

// SourceLocation is where a unit lives in one revision.
type SourceLocation struct {
	Path      string `json:"path"`
	Side      string `json:"side"` // "base" or "head"
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
}

// FileDiff holds the diff lines of one file that belong to a node.
type FileDiff struct {
	OldPath string     `json:"oldPath,omitempty"`
	NewPath string     `json:"newPath,omitempty"`
	Binary  bool       `json:"binary,omitempty"`
	Lines   []DiffLine `json:"lines,omitempty"`
}

// Node is one entry of the compressed tree.
type Node struct {
	ID                string   `json:"id"`
	Kind              UnitKind `json:"kind"`
	Name              string   `json:"name"`
	PreviousName      string   `json:"previousName,omitempty"`
	Signature         string   `json:"signature,omitempty"`
	PreviousSignature string   `json:"previousSignature,omitempty"`
	Op                ChangeOp `json:"op"`
	// Direct is false when the node appears only because a descendant changed.
	Direct    bool             `json:"direct"`
	Moved     bool             `json:"moved,omitempty"`
	Test      bool             `json:"test,omitempty"`
	Locations []SourceLocation `json:"locations,omitempty"`
	Added     int              `json:"added"`
	Removed   int              `json:"removed"`
	Summary   Summary          `json:"summary"`
	Outcomes  []string         `json:"outcomes,omitempty"`
	Diff      []FileDiff       `json:"diff,omitempty"`
	Children  []*Node          `json:"children,omitempty"`

	// Doc and Source are model context only, and stay out of the published representation.
	Doc string `json:"-"`
	// Note is a structural fact a model summary should take into account.
	Note   string `json:"-"`
	Source string `json:"-"`
}

// Walk visits the node and its descendants, parents first.
func (n *Node) Walk(visit func(*Node)) {
	visit(n)
	for _, c := range n.Children {
		c.Walk(visit)
	}
}

// Outcome is an externally meaningful result of the change. It is a lens on the tree: it
// references nodes and never alters them.
type Outcome struct {
	ID          string     `json:"id"`
	Op          ChangeOp   `json:"op"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	NodeIDs     []string   `json:"nodeIds"`
	Confidence  Confidence `json:"confidence,omitempty"`
	Uncertainty string     `json:"uncertainty,omitempty"`
}

// Coverage is the accountability report for I1 and I7.
type Coverage struct {
	ChangedFiles int      `json:"changedFiles"`
	TracedFiles  int      `json:"tracedFiles"`
	ChangedLines int      `json:"changedLines"`
	TracedLines  int      `json:"tracedLines"`
	Untraced     []string `json:"untraced,omitempty"`
}

// Complete reports whether every changed file and line traces to a node.
func (c Coverage) Complete() bool {
	return c.ChangedFiles == c.TracedFiles && c.ChangedLines == c.TracedLines
}

// PromptInfo identifies one prompt used by a run.
type PromptInfo struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// Generation records how a compressed diff was produced, so runs and prompt variants can be
// compared.
type Generation struct {
	Model         string        `json:"model"`
	PromptVariant string        `json:"promptVariant"`
	Prompts       []PromptInfo  `json:"prompts,omitempty"`
	ModelCalls    int           `json:"modelCalls"`
	CacheHits     int           `json:"cacheHits"`
	FailedCalls   int           `json:"failedCalls"`
	InputTokens   int64         `json:"inputTokens"`
	OutputTokens  int64         `json:"outputTokens"`
	CostUSD       float64       `json:"costUsd"`
	Duration      time.Duration `json:"durationNs"`
	// Provider and ReadingLevel are empty for diffs saved before they were recorded.
	Provider     ModelProvider `json:"provider,omitempty"`
	ReadingLevel ReadingLevel  `json:"readingLevel,omitempty"`
}

// Metrics are the measures used to compare runs and prompt variants.
type Metrics struct {
	SourceDiffChars int `json:"sourceDiffChars"`
	CompressedChars int `json:"compressedChars"`
	// CompressionRatio compares the source change with what the opening view shows.
	CompressionRatio float64 `json:"compressionRatio"`
	// FullTreeRatio compares it with the whole tree, fully expanded.
	FullTreeRatio    float64 `json:"fullTreeRatio"`
	Nodes            int     `json:"nodes"`
	ModelSummaries   int     `json:"modelSummaries"`
	LowConfidence    int     `json:"lowConfidence"`
	MissingSummaries int     `json:"missingSummaries"`
	Outcomes         int     `json:"outcomes"`
	UnassignedLeaves int     `json:"unassignedLeaves"`
}

// CompressedDiff is the canonical product of a run. Views are derived from it.
type CompressedDiff struct {
	SchemaVersion int       `json:"schemaVersion"`
	Repository    string    `json:"repository"`
	Base          string    `json:"base"`
	Head          string    `json:"head"`
	BaseSHA       string    `json:"baseSha"`
	HeadSHA       string    `json:"headSha"`
	Overview      string    `json:"overview,omitempty"`
	Outcomes      []Outcome `json:"outcomes"`
	Nodes         []*Node   `json:"nodes"`
	// Unassigned lists changed nodes no outcome reaches.
	Unassigned []string   `json:"unassigned"`
	Files      int        `json:"files"`
	Added      int        `json:"added"`
	Removed    int        `json:"removed"`
	Coverage   Coverage   `json:"coverage"`
	Generation Generation `json:"generation"`
	Metrics    Metrics    `json:"metrics"`
	Warnings   []string   `json:"warnings,omitempty"`
}
