package cli

import (
	"context"

	"github.com/arrno/diff-duck/internal/adapters/goanalysis"
	"github.com/arrno/diff-duck/internal/adapters/treesitter"
	"github.com/arrno/diff-duck/internal/services/ports"
)

// buildAnalyzers returns one analyzer per supported language. Grammars compile on first use,
// so languages a diff never touches cost nothing. Call the returned function when finished.
func buildAnalyzers(ctx context.Context) ([]ports.SourceAnalyzer, func(), error) {
	rt, err := treesitter.NewRuntime(ctx)
	if err != nil {
		return nil, nil, err
	}
	analyzers := []ports.SourceAnalyzer{
		goanalysis.New(),
		treesitter.NewPython(rt),
		treesitter.NewRust(rt),
	}
	return analyzers, func() { _ = rt.Close(context.Background()) }, nil
}
