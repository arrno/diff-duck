package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services/ports"
)

// ErrNoExplanations means every model call failed. The structural diff is still returned, so
// callers can publish it while reporting the failure.
var ErrNoExplanations = errors.New("every model call failed, so the output has no generated explanations")

// CompressRequest identifies the change to compress.
type CompressRequest struct {
	Repository string
	Base       string
	Head       string
	// StructureOnly skips the model entirely and produces only deterministic facts.
	StructureOnly bool
	// MaxModelCalls limits model calls; 0 means no limit. Work beyond it stays visibly unexplained.
	MaxModelCalls int
	// Progress, when set, receives a plain description of each stage as it starts.
	Progress func(stage string)
	// ReadingLevel is how plainly the model writes. It is required unless the run is
	// structure only.
	ReadingLevel domain.ReadingLevel
	// Provider is recorded with the run; it is empty for a model no person can choose.
	Provider domain.ModelProvider
}

func (r CompressRequest) report(stage string) {
	if r.Progress != nil {
		r.Progress(stage)
	}
}

// DiffCompressor runs the pipeline from a commit pair to a compressed diff.
type DiffCompressor struct {
	repo        ports.RepositoryReader
	differ      *StructuralDiffer
	model       ports.LanguageModel
	prompts     ports.PromptSource
	concurrency int
	now         func() time.Time
}

// NewDiffCompressor returns the pipeline. Prompts and model may be nil for structure-only runs.
func NewDiffCompressor(repo ports.RepositoryReader, differ *StructuralDiffer, model ports.LanguageModel, prompts ports.PromptSource, concurrency int) *DiffCompressor {
	return &DiffCompressor{repo: repo, differ: differ, model: model, prompts: prompts, concurrency: concurrency, now: time.Now}
}

// Compress produces the compressed diff. With ErrNoExplanations the returned diff is still
// valid, holding only structural facts.
func (c *DiffCompressor) Compress(ctx context.Context, req CompressRequest) (*domain.CompressedDiff, error) {
	started := c.now()

	// Load prompts first, so a bad variant fails before anything is spent.
	var system map[string]string
	var infos []domain.PromptInfo
	if !req.StructureOnly {
		if c.model == nil || c.prompts == nil {
			return nil, errors.New("a model and prompts are required unless the run is structure only")
		}
		var err error
		system, infos, err = c.loadPrompts(req.ReadingLevel)
		if err != nil {
			return nil, err
		}
	}

	req.report("Reading the changes")
	baseSHA, err := c.repo.ResolveCommit(ctx, req.Base)
	if err != nil {
		return nil, err
	}
	headSHA, err := c.repo.ResolveCommit(ctx, req.Head)
	if err != nil {
		return nil, err
	}
	changes, err := c.repo.ChangedFiles(ctx, baseSHA, headSHA)
	if err != nil {
		return nil, err
	}
	roots, warnings, err := c.differ.Build(ctx, baseSHA, headSHA, changes)
	if err != nil {
		return nil, err
	}

	// Published as an empty list, never null, so an empty diff is an ordinary result.
	if roots == nil {
		roots = []*domain.Node{}
	}
	result := &domain.CompressedDiff{
		SchemaVersion: 1,
		Repository:    req.Repository,
		Base:          req.Base,
		Head:          req.Head,
		BaseSHA:       baseSHA,
		HeadSHA:       headSHA,
		Nodes:         roots,
		Outcomes:      []domain.Outcome{},
		Files:         len(changes),
		Coverage:      domain.MeasureCoverage(changes, roots),
		Warnings:      warnings,
	}
	if !result.Coverage.Complete() {
		result.Warnings = append(result.Warnings, fmt.Sprintf("coverage is incomplete: %d of %d changed lines and %d of %d files trace to a node",
			result.Coverage.TracedLines, result.Coverage.ChangedLines, result.Coverage.TracedFiles, result.Coverage.ChangedFiles))
	}
	// Totals come from the diff itself, the same source a reader would check them against.
	for _, f := range changes {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				switch l.Kind {
				case domain.LineAdded:
					result.Added++
				case domain.LineRemoved:
					result.Removed++
				}
			}
		}
	}

	result.Generation.Model = "none (structure only)"
	result.Generation.PromptVariant = "none"
	var runner *modelRunner
	if !req.StructureOnly {
		result.Generation.Model = c.model.Describe()
		result.Generation.PromptVariant = c.prompts.Variant()
		result.Generation.Prompts = infos
		runner = &modelRunner{model: c.model}
		// Keep one call back for outcomes, which are the most valuable output of a run.
		if req.MaxModelCalls > 0 {
			forSummaries := req.MaxModelCalls - 1
			if forSummaries == 0 {
				forSummaries = -1
			}
			runner.setLimit(forSummaries)
		}
		compressor := &semanticCompressor{
			runner:       runner,
			leafPrompt:   system[promptLeaf],
			parentPrompt: system[promptParent],
			concurrency:  c.concurrency,
		}
		req.report("Explaining the code")
		result.Warnings = append(result.Warnings, compressor.Compress(ctx, roots)...)
		runner.setLimit(req.MaxModelCalls)
		req.report("Finding the outcomes")
		identifyOutcomes(ctx, runner, system[promptOutcomes], result)
	}

	kept, unassigned, rejected := domain.AssociateOutcomes(roots, result.Outcomes)
	result.Outcomes, result.Unassigned = kept, unassigned
	result.Warnings = append(result.Warnings, rejected...)

	if runner != nil {
		stats := runner.stats
		stats.Model, stats.PromptVariant, stats.Prompts = result.Generation.Model, result.Generation.PromptVariant, infos
		result.Generation = stats
		// Set after the swap above, which would otherwise drop them.
		result.Generation.Provider, result.Generation.ReadingLevel = req.Provider, req.ReadingLevel
	}
	result.Generation.Duration = c.now().Sub(started)
	result.Metrics = measure(result, changes)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g := result.Generation
	if !req.StructureOnly && g.CacheHits == 0 && g.ModelCalls > 0 && g.FailedCalls == g.ModelCalls {
		return result, ErrNoExplanations
	}
	return result, nil
}

// loadPrompts returns every system prompt with the reading level's instruction appended, so
// the level is part of each prompt and therefore of each cached answer's identity (K3).
func (c *DiffCompressor) loadPrompts(level domain.ReadingLevel) (map[string]string, []domain.PromptInfo, error) {
	if _, err := domain.ParseReadingLevel(string(level)); err != nil {
		return nil, nil, err
	}
	instruction, levelInfo, err := c.prompts.LoadReadingLevel(level)
	if err != nil {
		return nil, nil, err
	}
	system := map[string]string{}
	var infos []domain.PromptInfo
	for _, name := range []string{promptLeaf, promptParent, promptOutcomes} {
		text, info, err := c.prompts.Load(name)
		if err != nil {
			return nil, nil, err
		}
		system[name] = strings.TrimRight(text, "\n") + "\n\n" + instruction
		infos = append(infos, info)
	}
	return system, append(infos, levelInfo), nil
}

func measure(result *domain.CompressedDiff, changes []domain.FileChange) domain.Metrics {
	var m domain.Metrics
	for _, f := range changes {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.IsChange() {
					m.SourceDiffChars += len(l.Text) + 1
				}
			}
		}
	}
	shown := func(n *domain.Node) int { return len(n.Name) + len(n.Signature) + len(n.Summary.Text) }
	opening := len(result.Overview)
	for _, o := range result.Outcomes {
		opening += len(o.Title) + len(o.Description)
	}
	full := opening
	for _, r := range result.Nodes {
		// The viewer opens with each root expanded, so these are what a reader first sees.
		opening += shown(r)
		for _, c := range r.Children {
			opening += shown(c)
		}
		r.Walk(func(n *domain.Node) {
			m.Nodes++
			full += shown(n)
			switch n.Summary.Origin {
			case domain.OriginModel:
				m.ModelSummaries++
			case domain.OriginNone:
				m.MissingSummaries++
			}
			if n.Summary.Confidence == domain.ConfidenceLow {
				m.LowConfidence++
			}
		})
	}
	m.CompressedChars = opening
	if opening > 0 {
		m.CompressionRatio = float64(m.SourceDiffChars) / float64(opening)
	}
	if full > 0 {
		m.FullTreeRatio = float64(m.SourceDiffChars) / float64(full)
	}
	m.Outcomes = len(result.Outcomes)
	m.UnassignedLeaves = len(result.Unassigned)
	return m
}
