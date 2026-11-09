package services

import (
	"context"

	"github.com/arrno/diff-duck/internal/domain"
)

// dirPackages is what is known of the packages a directory's files declare at one commit.
type dirPackages struct {
	pending []string // files not analyzed yet
	refs    map[domain.PackageRef]bool
	// unclear means some file could not be listed, read, or analyzed, so no package can be
	// shown absent there.
	unclear bool
}

// declaredPackages returns a lookup of whether a package has source at a commit, for one diff.
// Anything unclear counts as existing, since claiming a package was added or removed needs
// positive evidence. A directory is listed once per commit, and its files are analyzed lazily,
// each at most once, stopping as soon as one declares the package asked about. A file can
// declare many packages (C# namespaces), so analyzing the directory again for each package
// would cost packages times files parses.
func (d *StructuralDiffer) declaredPackages(ctx context.Context) func(sha string, ref domain.PackageRef) bool {
	type at struct{ sha, dir string }
	dirs := map[at]*dirPackages{}
	return func(sha string, ref domain.PackageRef) bool {
		p := dirs[at{sha, ref.Dir}]
		if p == nil {
			paths, err := d.repo.ListFiles(ctx, sha, ref.Dir)
			p = &dirPackages{pending: paths, refs: map[domain.PackageRef]bool{}, unclear: err != nil}
			dirs[at{sha, ref.Dir}] = p
		}
		for !p.unclear && !p.refs[ref] && len(p.pending) > 0 {
			next := p.pending[0]
			p.pending = p.pending[1:]
			p.unclear = !d.declare(ctx, sha, next, p.refs)
		}
		return p.unclear || p.refs[ref]
	}
}

// declare records the packages a file declares, analyzed as a change to it would be. It
// reports false when the file could not be read or analyzed.
func (d *StructuralDiffer) declare(ctx context.Context, sha, p string, refs map[domain.PackageRef]bool) bool {
	analyzer := d.analyzerFor(domain.FileChange{NewPath: p})
	if analyzer == nil {
		return true
	}
	src, err := d.repo.ReadFile(ctx, sha, p)
	if err != nil {
		return false
	}
	analysis, err := analyzer.Analyze(ctx, p, src)
	if err != nil {
		return false
	}
	refs[analysis.Package] = true
	// A file can declare several packages, such as C# namespaces; each unit names its own.
	for _, u := range analysis.Units {
		refs[u.Package] = true
	}
	return true
}
