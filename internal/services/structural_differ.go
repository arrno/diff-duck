package services

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"duck-diff/internal/domain"
	"duck-diff/internal/services/ports"
)

// contextLines is how many unchanged lines surround a change in a node's diff excerpt.
const contextLines = 2

// StructuralDiffer derives the changed code unit tree from source analysis alone. Everything
// it produces is a deterministic fact (I11); summaries come later and cannot alter it.
type StructuralDiffer struct {
	repo      ports.RepositoryReader
	analyzers []ports.SourceAnalyzer
}

// NewStructuralDiffer returns a differ using the given analyzers.
func NewStructuralDiffer(repo ports.RepositoryReader, analyzers ...ports.SourceAnalyzer) *StructuralDiffer {
	return &StructuralDiffer{repo: repo, analyzers: analyzers}
}

// keyedUnit is a unit with the identity used to match it across revisions.
type keyedUnit struct {
	domain.Unit
	key string
}

type analyzedFile struct {
	change        domain.FileChange
	pkg           domain.PackageRef
	generated     bool
	before, after []*keyedUnit
	test          bool
	oldConstraint string
	newConstraint string
}

type treeBuilder struct {
	nodes    map[string]*domain.Node // by ID
	packages map[string]*domain.Node
	nodeFor  map[string]*domain.Node // unit key to node; a renamed unit's old key maps here too
	parents  map[string]*domain.Node // node ID to parent
	roots    []*domain.Node
	refs     map[string]domain.PackageRef
	// unitParent maps a unit's key to its enclosing unit's key.
	unitParent map[string]string
}

// Build returns the tree's root nodes and any warnings about files it could not analyze.
func (d *StructuralDiffer) Build(ctx context.Context, baseSHA, headSHA string, changes []domain.FileChange) ([]*domain.Node, []string, error) {
	b := &treeBuilder{
		nodes:      map[string]*domain.Node{},
		packages:   map[string]*domain.Node{},
		nodeFor:    map[string]*domain.Node{},
		parents:    map[string]*domain.Node{},
		refs:       map[string]domain.PackageRef{},
		unitParent: map[string]string{},
	}
	var warnings []string
	var files []*analyzedFile
	for _, change := range changes {
		file, reason, err := d.analyze(ctx, baseSHA, headSHA, change)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case file != nil && file.generated:
			b.addRoot(fileNode(change, explainFile(change, true)))
		case file != nil:
			files = append(files, file)
		case reason != nil:
			warnings = append(warnings, fmt.Sprintf("%s: analyzed as a plain file: %v", change.Path(), reason))
			b.addRoot(fileNode(change, fileExplanation{uncertainty: "This file could not be parsed, so its contents are not broken into code units."}))
		default:
			b.addRoot(fileNode(change, explainFile(change, isGeneratedPath(change.Path()))))
		}
	}

	assignKeys(files)
	b.diffUnits(files)
	for _, f := range files {
		b.attributeLines(f)
	}
	b.finish(func(ref domain.PackageRef, op domain.ChangeOp) bool {
		sha := baseSHA
		if op == domain.OpRemove {
			sha = headSHA
		}
		return d.packageExists(ctx, sha, ref)
	})
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return b.roots, warnings, nil
}

// analyze returns the parsed file when it can be represented as code units. A nil file with a
// nil reason means it belongs in a plain file node by policy; a non-nil reason means analysis
// failed.
func (d *StructuralDiffer) analyze(ctx context.Context, baseSHA, headSHA string, change domain.FileChange) (*analyzedFile, error, error) {
	if change.Binary || isGeneratedPath(change.Path()) {
		return nil, nil, nil
	}
	analyzer := d.analyzerFor(change)
	if analyzer == nil {
		return nil, nil, nil
	}
	file := &analyzedFile{change: change}
	generated := false
	read := func(sha, p string) (domain.FileAnalysis, error, error) {
		src, err := d.repo.ReadFile(ctx, sha, p)
		if err != nil {
			if ctx.Err() != nil {
				return domain.FileAnalysis{}, nil, ctx.Err()
			}
			return domain.FileAnalysis{}, err, nil
		}
		analysis, err := analyzer.Analyze(p, src)
		return analysis, err, nil
	}
	if change.OldPath != "" {
		a, reason, err := read(baseSHA, change.OldPath)
		if err != nil || reason != nil {
			return nil, reason, err
		}
		file.pkg, generated, file.test = a.Package, a.Generated, a.Test
		file.oldConstraint = a.Constraints
		file.before = keyedUnits(a.Units)
	}
	if change.NewPath != "" {
		a, reason, err := read(headSHA, change.NewPath)
		if err != nil || reason != nil {
			return nil, reason, err
		}
		file.pkg, generated, file.test = a.Package, generated || a.Generated, a.Test
		file.newConstraint = a.Constraints
		file.after = keyedUnits(a.Units)
	}
	file.generated = generated
	return file, nil, nil
}

// packageExists reports whether a package has source at a commit. Anything unclear counts as
// existing, since claiming a package was added or removed needs positive evidence.
func (d *StructuralDiffer) packageExists(ctx context.Context, sha string, ref domain.PackageRef) bool {
	paths, err := d.repo.ListFiles(ctx, sha, ref.Dir)
	if err != nil {
		return true
	}
	for _, p := range paths {
		for _, analyzer := range d.analyzers {
			if !analyzer.Supports(p) {
				continue
			}
			src, err := d.repo.ReadFile(ctx, sha, p)
			if err != nil {
				return true
			}
			analysis, err := analyzer.Analyze(p, src)
			if err != nil || analysis.Package == ref {
				return true
			}
		}
	}
	return false
}

func (d *StructuralDiffer) analyzerFor(change domain.FileChange) ports.SourceAnalyzer {
	for _, a := range d.analyzers {
		oldOK := change.OldPath == "" || a.Supports(change.OldPath)
		newOK := change.NewPath == "" || a.Supports(change.NewPath)
		if oldOK && newOK {
			return a
		}
	}
	return nil
}

func keyedUnits(units []domain.Unit) []*keyedUnit {
	out := make([]*keyedUnit, len(units))
	for i := range units {
		out[i] = &keyedUnit{Unit: units[i]}
	}
	return out
}

// assignKeys gives each unit its cross-revision identity. Most names are unique within a
// package. Those that are not, such as init functions or build-tagged variants, are paired
// across revisions by content before position, so inserting one never relabels the others.
func assignKeys(files []*analyzedFile) {
	type group struct{ before, after []*keyedUnit }
	groups := map[string]*group{}
	get := func(u *keyedUnit) *group {
		key := u.Package.Key() + "::" + u.Name
		if groups[key] == nil {
			groups[key] = &group{}
		}
		return groups[key]
	}
	for _, f := range files {
		for _, u := range f.before {
			g := get(u)
			g.before = append(g.before, u)
		}
		for _, u := range f.after {
			g := get(u)
			g.after = append(g.after, u)
		}
	}
	for plain, g := range groups {
		if len(g.before) <= 1 && len(g.after) <= 1 {
			for _, u := range append(g.before, g.after...) {
				u.key = plain
			}
			continue
		}
		pairDuplicates(plain, g.before, g.after)
	}
}

func pairDuplicates(plain string, before, after []*keyedUnit) {
	next := 0
	assign := func(units ...*keyedUnit) {
		key := fmt.Sprintf("%s#%s#%d", plain, path.Base(units[0].File), next)
		next++
		for _, u := range units {
			u.key = key
		}
	}
	pair := func(match func(old, cur *keyedUnit) bool) {
		for _, old := range before {
			if old.key != "" {
				continue
			}
			for _, cur := range after {
				if cur.key == "" && match(old, cur) {
					assign(old, cur)
					break
				}
			}
		}
	}
	pair(func(old, cur *keyedUnit) bool { return old.Hash == cur.Hash && old.File == cur.File })
	pair(func(old, cur *keyedUnit) bool { return old.Hash == cur.Hash })
	pair(func(old, cur *keyedUnit) bool { return old.File == cur.File })
	for _, u := range append(append([]*keyedUnit{}, before...), after...) {
		if u.key == "" {
			assign(u)
		}
	}
}

func (b *treeBuilder) diffUnits(files []*analyzedFile) {
	before, after := map[string]*keyedUnit{}, map[string]*keyedUnit{}
	byName := map[string][]*keyedUnit{} // for resolving a parent's key
	for _, f := range files {
		for _, u := range f.before {
			before[u.key] = u
			byName[u.Package.Key()+"::"+u.Name] = append(byName[u.Package.Key()+"::"+u.Name], u)
		}
		for _, u := range f.after {
			after[u.key] = u
			byName[u.Package.Key()+"::"+u.Name] = append(byName[u.Package.Key()+"::"+u.Name], u)
		}
	}

	var changed []*keyedUnit
	for _, key := range sortedKeys(before, after) {
		old, cur := before[key], after[key]
		switch {
		case old == nil:
			b.register(key, newUnitNode(cur, domain.OpAdd))
			changed = append(changed, cur)
		case cur == nil:
			b.register(key, newUnitNode(old, domain.OpRemove))
			changed = append(changed, old)
		case old.Hash != cur.Hash:
			n := newUnitNode(cur, domain.OpChange)
			addLocation(n, old, "base")
			if old.Signature != cur.Signature {
				n.PreviousSignature = old.Signature
			} else if cur.ChangeNote != "" {
				n.Summary = domain.Summary{Origin: domain.OriginStructural, Text: cur.ChangeNote}
			}
			b.register(key, n)
			changed = append(changed, cur)
		case old.File != cur.File:
			n := newUnitNode(cur, domain.OpChange)
			addLocation(n, old, "base")
			n.Moved = true
			n.Summary = domain.Summary{
				Origin: domain.OriginStructural,
				Text:   fmt.Sprintf("Moved from %s to %s with no code change.", old.File, cur.File),
			}
			b.register(key, n)
			changed = append(changed, cur)
		}
	}

	changed = append(changed, b.detectReorderedFields(files, before, after)...)
	changed = b.followRenamedFiles(files, changed)
	changed = b.detectRenames(changed)
	for _, f := range files {
		for _, side := range [][]*keyedUnit{f.before, f.after} {
			for _, u := range side {
				if u.Parent != "" {
					b.unitParent[u.key] = u.Package.Key() + "::" + u.Parent
				}
			}
		}
	}

	for _, u := range changed {
		b.attach(u, before, after, byName)
	}
}

func sortedKeys(maps ...map[string]*keyedUnit) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range maps {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// detectRenames recognizes a function whose parameters, results, and body are identical under
// a new name. A match counts only when it is the single candidate on both sides. Everything
// else stays REMOVE plus ADD rather than inventing continuity (I10).
func (b *treeBuilder) detectRenames(changed []*keyedUnit) []*keyedUnit {
	removed, added := map[string][]*keyedUnit{}, map[string][]*keyedUnit{}
	for _, u := range changed {
		if u.BodyHash == "" {
			continue
		}
		match := string(u.Kind) + "|" + u.BodyHash
		n := b.nodeFor[u.key]
		if n.Op == domain.OpRemove {
			removed[match] = append(removed[match], u)
		} else if n.Op == domain.OpAdd {
			added[match] = append(added[match], u)
		}
	}
	dropped := map[string]bool{}
	for match, olds := range removed {
		news := added[match]
		if len(olds) != 1 || len(news) != 1 {
			continue
		}
		old, cur := olds[0], news[0]
		n := b.nodeFor[cur.key]
		n.Op = domain.OpChange
		n.PreviousName = old.Name
		text := fmt.Sprintf("Renamed from %s with an identical body.", old.Name)
		if old.Package != cur.Package {
			n.Moved = true
			n.PreviousName = old.Package.Dir + "." + old.Name
			text = fmt.Sprintf("Moved from package %s with an identical body.", old.Package.Dir)
			if old.Name != cur.Name {
				text = fmt.Sprintf("Moved and renamed from %s with an identical body.", n.PreviousName)
			}
		}
		if old.Signature != cur.Signature {
			n.PreviousSignature = old.Signature
		}
		addLocation(n, old, "base")
		n.Summary = domain.Summary{Origin: domain.OriginStructural, Text: text}
		b.absorb(old, n)
		dropped[old.key] = true
	}
	kept := changed[:0]
	for _, u := range changed {
		if !dropped[u.key] {
			kept = append(kept, u)
		}
	}
	return kept
}

// absorb makes a surviving node the owner of a unit that turned out to be its earlier identity.
func (b *treeBuilder) absorb(old *keyedUnit, n *domain.Node) {
	delete(b.nodes, b.nodeFor[old.key].ID)
	b.nodeFor[old.key] = n
}

// followRenamedFiles keeps identity when git reports a file as renamed into another package,
// such as a directory rename. A unit with the same name in the renamed file is the same unit.
func (b *treeBuilder) followRenamedFiles(files []*analyzedFile, changed []*keyedUnit) []*keyedUnit {
	renamedTo := map[string]string{}
	for _, f := range files {
		if f.change.Status == domain.FileRenamed {
			renamedTo[f.change.OldPath] = f.change.NewPath
		}
	}
	if len(renamedTo) == 0 {
		return changed
	}
	type place struct {
		file, name string
		kind       domain.UnitKind
	}
	added := map[place]*keyedUnit{}
	for _, u := range changed {
		if b.nodeFor[u.key].Op == domain.OpAdd {
			added[place{u.File, u.Name, u.Kind}] = u
		}
	}
	dropped := map[string]bool{}
	for _, old := range changed {
		if b.nodeFor[old.key].Op != domain.OpRemove {
			continue
		}
		cur := added[place{renamedTo[old.File], old.Name, old.Kind}]
		if cur == nil {
			continue
		}
		n := b.nodeFor[cur.key]
		n.Op = domain.OpChange
		n.Moved = true
		n.PreviousName = old.Package.Dir + "." + old.Name
		if old.Signature != cur.Signature {
			n.PreviousSignature = old.Signature
		}
		addLocation(n, old, "base")
		if old.Hash == cur.Hash {
			n.Summary = domain.Summary{
				Origin: domain.OriginStructural,
				Text:   fmt.Sprintf("Moved from %s to %s with no code change.", old.File, cur.File),
			}
		}
		b.absorb(old, n)
		dropped[old.key] = true
	}
	kept := changed[:0]
	for _, u := range changed {
		if !dropped[u.key] {
			kept = append(kept, u)
		}
	}
	return kept
}

// detectReorderedFields finds structs whose surviving fields changed relative order. No field
// changed, so nothing else would notice, yet this changes memory layout and positional literals.
func (b *treeBuilder) detectReorderedFields(files []*analyzedFile, before, after map[string]*keyedUnit) []*keyedUnit {
	order := func(side func(*analyzedFile) []*keyedUnit) map[string][]string {
		fields := map[string][]string{}
		for _, f := range files {
			for _, u := range side(f) {
				if u.Kind == domain.KindField && u.Parent != "" {
					key := u.Package.Key() + "::" + u.Parent
					fields[key] = append(fields[key], u.Name)
				}
			}
		}
		return fields
	}
	oldFields := order(func(f *analyzedFile) []*keyedUnit { return f.before })
	newFields := order(func(f *analyzedFile) []*keyedUnit { return f.after })

	var keys []string
	for key := range oldFields {
		if newFields[key] != nil {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var added []*keyedUnit
	for _, key := range keys {
		cur := after[key]
		if cur == nil || before[key] == nil || cur.Kind != domain.KindStruct || sameRelativeOrder(oldFields[key], newFields[key]) {
			continue
		}
		n, exists := b.nodeFor[key]
		if !exists {
			n = newUnitNode(cur, domain.OpChange)
			addLocation(n, before[key], "base")
			b.register(key, n)
			added = append(added, cur)
		}
		n.Direct = true
		n.Note = "Field order changed."
	}
	return added
}

func sameRelativeOrder(a, b []string) bool {
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, s := range a {
		inA[s] = true
	}
	for _, s := range b {
		inB[s] = true
	}
	var commonA, commonB []string
	for _, s := range a {
		if inB[s] {
			commonA = append(commonA, s)
		}
	}
	for _, s := range b {
		if inA[s] {
			commonB = append(commonB, s)
		}
	}
	return slices.Equal(commonA, commonB)
}

func (b *treeBuilder) register(key string, n *domain.Node) {
	n.ID = key
	b.nodes[key] = n
	b.nodeFor[key] = n
}

// attach places a changed unit's node under its structural parent, creating the ancestors
// needed to show where it lives. An ancestor created only for that reason is inherited.
func (b *treeBuilder) attach(u *keyedUnit, before, after map[string]*keyedUnit, byName map[string][]*keyedUnit) {
	n := b.nodeFor[u.key]
	var parent *domain.Node
	if u.Parent != "" {
		parent = b.typeAncestor(u, before, after, byName)
	} else {
		parent = b.packageNode(u.Package)
	}
	link(b, parent, n)
}

func (b *treeBuilder) typeAncestor(u *keyedUnit, before, after map[string]*keyedUnit, byName map[string][]*keyedUnit) *domain.Node {
	pkg := u.Package.Key()
	base := pkg + "::" + u.Parent
	key := base
	var unit *keyedUnit
	// Prefer the declaration in the same file when the name is ambiguous.
	candidates := byName[base]
	for _, c := range candidates {
		if c.File == u.File {
			unit = c
			break
		}
	}
	if unit == nil && len(candidates) > 0 {
		unit = candidates[0]
	}
	if unit != nil {
		key = unit.key
	}
	if existing, ok := b.nodeFor[key]; ok {
		if _, attached := b.parents[existing.ID]; !attached {
			link(b, b.packageNode(u.Package), existing)
		}
		return existing
	}
	// Prefer the current declaration for display when one exists.
	if cur := after[key]; cur != nil {
		unit = cur
	} else if old := before[key]; old != nil {
		unit = old
	}
	n := &domain.Node{Name: u.Parent, Kind: domain.KindType, Op: domain.OpChange, Test: u.Test}
	if unit != nil {
		n.Kind = unit.Kind
		n.Signature = unit.Signature
		n.Doc = unit.Doc
		n.Test = unit.Test
		// The location shows where the type is declared, even if nothing changed there.
		side := "head"
		if after[key] == nil {
			side = "base"
		}
		addLocation(n, unit, side)
	}
	b.register(key, n)
	link(b, b.packageNode(u.Package), n)
	return n
}

func (b *treeBuilder) packageNode(ref domain.PackageRef) *domain.Node {
	key := ref.Key()
	if n, ok := b.packages[key]; ok {
		return n
	}
	n := &domain.Node{
		ID:        key,
		Kind:      domain.KindPackage,
		Name:      ref.Dir,
		Signature: "package " + ref.Name,
		Op:        domain.OpChange,
	}
	b.packages[key] = n
	b.refs[key] = ref
	b.nodes[key] = n
	b.roots = append(b.roots, n)
	return n
}

func (b *treeBuilder) addRoot(n *domain.Node) {
	b.nodes[n.ID] = n
	b.roots = append(b.roots, n)
}

func link(b *treeBuilder, parent, child *domain.Node) {
	if _, attached := b.parents[child.ID]; attached {
		return
	}
	b.parents[child.ID] = parent
	parent.Children = append(parent.Children, child)
}

func newUnitNode(u *keyedUnit, op domain.ChangeOp) *domain.Node {
	n := &domain.Node{
		Kind:      u.Kind,
		Name:      u.Name,
		Signature: u.Signature,
		Op:        op,
		Direct:    true,
		Test:      u.Test,
		Doc:       u.Doc,
		Source:    u.Source,
	}
	side := "head"
	if op == domain.OpRemove {
		side = "base"
	}
	addLocation(n, u, side)
	return n
}

func addLocation(n *domain.Node, u *keyedUnit, side string) {
	n.Locations = append(n.Locations, domain.SourceLocation{Path: u.File, Side: side, StartLine: u.StartLine, EndLine: u.EndLine})
}

// paintOwners maps each line to the units that most tightly contain it. Units sharing an exact
// range, such as the fields in "a, b int", own the line together.
func paintOwners(units []*keyedUnit) map[int][]string {
	sorted := append([]*keyedUnit(nil), units...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].EndLine-sorted[i].StartLine > sorted[j].EndLine-sorted[j].StartLine
	})
	type span struct{ start, end int }
	owners := map[int][]string{}
	painted := map[int]span{}
	for _, u := range sorted {
		s := span{u.StartLine, u.EndLine}
		for line := u.StartLine; line <= u.EndLine; line++ {
			if painted[line] == s {
				owners[line] = append(owners[line], u.key)
			} else {
				owners[line] = []string{u.key}
				painted[line] = s
			}
		}
	}
	return owners
}

// attributeLines assigns every changed line to exactly one owner: the innermost changed unit
// containing it, or else the file's region node, which accounts for imports, comments, and
// formatting. This is what guarantees complete change coverage (I1).
func (b *treeBuilder) attributeLines(f *analyzedFile) {
	oldOwners, newOwners := paintOwners(f.before), paintOwners(f.after)

	touched := false
	for _, hunk := range f.change.Hunks {
		marks := map[*domain.Node][]bool{}
		mark := func(n *domain.Node, i int) {
			if marks[n] == nil {
				marks[n] = make([]bool, len(hunk.Lines))
			}
			marks[n][i] = true
		}
		for i, line := range hunk.Lines {
			if !line.IsChange() {
				continue
			}
			var keys []string
			if line.Kind == domain.LineAdded {
				keys = newOwners[line.NewLine]
			} else {
				keys = oldOwners[line.OldLine]
			}
			owned := false
			for _, key := range keys {
				if n, ok := b.nodeFor[key]; ok {
					mark(n, i)
					owned = true
				} else if n, ok := b.nodeFor[b.unitParent[key]]; ok && n.Note != "" {
					// An unchanged field's line moved because its struct was reordered.
					mark(n, i)
					owned = true
				}
			}
			if !owned {
				mark(b.regionNode(f), i)
			}
		}
		for n, m := range marks {
			addExcerpt(n, f.change, hunk, m)
			touched = true
		}
	}

	if f.oldConstraint != f.newConstraint && f.change.OldPath != "" && f.change.NewPath != "" {
		region := b.regionNode(f)
		region.Summary.Text = fmt.Sprintf("Build constraints changed from %q to %q, which can change whether this file compiles.", f.oldConstraint, f.newConstraint)
		touched = true
	}

	if !touched && !b.locates(f.change) {
		// A mode change or an identical copy has no lines and no moved units.
		b.addRoot(fileNode(f.change, explainFile(f.change, false)))
	}
}

// locates reports whether any node already points at this file.
func (b *treeBuilder) locates(change domain.FileChange) bool {
	for _, n := range b.nodes {
		for _, loc := range n.Locations {
			if loc.Path == change.NewPath || loc.Path == change.OldPath {
				return true
			}
		}
	}
	return false
}

func (b *treeBuilder) regionNode(f *analyzedFile) *domain.Node {
	id := "region:" + f.change.Path()
	if n, ok := b.nodes[id]; ok {
		return n
	}
	n := &domain.Node{
		ID:     id,
		Kind:   domain.KindRegion,
		Name:   path.Base(f.change.Path()),
		Op:     opForStatus(f.change.Status),
		Direct: true,
		Test:   f.test,
		Locations: []domain.SourceLocation{{
			Path: f.change.Path(),
			Side: sideForStatus(f.change.Status),
		}},
		Summary: domain.Summary{
			Origin: domain.OriginStructural,
			Text:   "Lines outside any changed code unit, such as the package clause, imports, comments, or formatting.",
		},
	}
	b.nodes[id] = n
	link(b, b.packageNode(f.pkg), n)
	return n
}

// addExcerpt appends the marked lines of a hunk, with nearby context, to the node's diff.
func addExcerpt(n *domain.Node, change domain.FileChange, hunk domain.Hunk, marked []bool) {
	keep := make([]bool, len(hunk.Lines))
	for i := range hunk.Lines {
		if !marked[i] {
			continue
		}
		keep[i] = true
		for j := i - 1; j >= 0 && j >= i-contextLines && !hunk.Lines[j].IsChange(); j-- {
			keep[j] = true
		}
		for j := i + 1; j < len(hunk.Lines) && j <= i+contextLines && !hunk.Lines[j].IsChange(); j++ {
			keep[j] = true
		}
	}

	var fd *domain.FileDiff
	for i := range n.Diff {
		if n.Diff[i].OldPath == change.OldPath && n.Diff[i].NewPath == change.NewPath {
			fd = &n.Diff[i]
		}
	}
	if fd == nil {
		n.Diff = append(n.Diff, domain.FileDiff{OldPath: change.OldPath, NewPath: change.NewPath})
		fd = &n.Diff[len(n.Diff)-1]
	}
	gap := len(fd.Lines) > 0
	for i, line := range hunk.Lines {
		if !keep[i] {
			gap = len(fd.Lines) > 0
			continue
		}
		if gap {
			fd.Lines = append(fd.Lines, domain.DiffLine{Kind: domain.LineGap})
			gap = false
		}
		fd.Lines = append(fd.Lines, line)
	}
}

// finish sets rolled-up operations and stats, explains what needs no model, and orders the
// tree so output is stable.
func (b *treeBuilder) finish(packageExists func(domain.PackageRef, domain.ChangeOp) bool) {
	for _, root := range b.roots {
		domain.RollUpStats(root)
		if root.Kind == domain.KindPackage {
			root.Op = rollUpOp(root)
			// A package exists or not independently of what changed inside it: adding one
			// function to a package does not add the package.
			if root.Op == domain.OpAdd || root.Op == domain.OpRemove {
				if packageExists(b.refs[root.ID], root.Op) {
					root.Op = domain.OpChange
				}
			}
			if onlyRegions(root) {
				text := "Only lines outside code units changed, such as imports, comments, or formatting."
				if len(root.Children) == 1 {
					text = root.Children[0].Summary.Text
				}
				root.Summary = domain.Summary{Origin: domain.OriginStructural, Text: text}
			}
		}
		root.Walk(func(n *domain.Node) {
			explainUnit(n)
			sort.SliceStable(n.Children, func(i, j int) bool {
				a, c := n.Children[i], n.Children[j]
				if kindRank(a.Kind) != kindRank(c.Kind) {
					return kindRank(a.Kind) < kindRank(c.Kind)
				}
				return a.Name < c.Name
			})
		})
	}
	sort.SliceStable(b.roots, func(i, j int) bool {
		a, c := b.roots[i], b.roots[j]
		if (a.Kind == domain.KindPackage) != (c.Kind == domain.KindPackage) {
			return a.Kind == domain.KindPackage
		}
		return a.ID < c.ID
	})
}

func rollUpOp(pkg *domain.Node) domain.ChangeOp {
	var first domain.ChangeOp
	for _, c := range pkg.Children {
		if first == "" {
			first = c.Op
		} else if c.Op != first {
			return domain.OpChange
		}
	}
	if first == "" {
		return domain.OpChange
	}
	return first
}

func onlyRegions(n *domain.Node) bool {
	for _, c := range n.Children {
		if c.Kind != domain.KindRegion {
			return false
		}
	}
	return true
}

// explainUnit marks leaves whose signature already says everything. A field or constant needs
// no generated prose, so it costs no model call.
func explainUnit(n *domain.Node) {
	if n.Summary.Origin != "" {
		return
	}
	if n.Note != "" && len(n.Children) == 0 {
		n.Summary = domain.Summary{Origin: domain.OriginStructural, Text: n.Note}
		return
	}
	if len(n.Children) == 0 {
		switch n.Kind {
		case domain.KindField, domain.KindConst, domain.KindInterfaceMethod, domain.KindStruct, domain.KindInterface, domain.KindType:
			n.Summary.Origin = domain.OriginStructural
			return
		case domain.KindVar:
			if strings.Count(n.Source, "\n") < 3 {
				n.Summary.Origin = domain.OriginStructural
				return
			}
		}
	}
	n.Summary.Origin = domain.OriginNone
}

func kindRank(k domain.UnitKind) int {
	switch k {
	case domain.KindStruct, domain.KindInterface, domain.KindType:
		return 0
	case domain.KindFunction, domain.KindMethod, domain.KindInterfaceMethod:
		return 1
	case domain.KindField:
		return 2
	case domain.KindConst, domain.KindVar:
		return 3
	case domain.KindRegion:
		return 5
	}
	return 4
}

type fileExplanation struct {
	text        string
	uncertainty string
}

func explainFile(change domain.FileChange, generated bool) fileExplanation {
	switch {
	case change.Binary:
		return fileExplanation{text: "Binary file; contents are not shown."}
	case generated:
		return fileExplanation{text: "Generated, vendored, or lock file; not summarized."}
	case len(change.Hunks) > 0:
		return fileExplanation{}
	case change.Status == domain.FileRenamed:
		return fileExplanation{text: fmt.Sprintf("Renamed from %s with no content change.", change.OldPath)}
	case change.Status == domain.FileModified:
		return fileExplanation{text: "File mode or type changed; no content change."}
	}
	return fileExplanation{text: "Empty file."}
}

func fileNode(change domain.FileChange, why fileExplanation) *domain.Node {
	n := &domain.Node{
		ID:     "file:" + change.Path(),
		Kind:   domain.KindFile,
		Name:   change.Path(),
		Op:     opForStatus(change.Status),
		Direct: true,
		Locations: []domain.SourceLocation{{
			Path: change.Path(),
			Side: sideForStatus(change.Status),
		}},
	}
	if change.Status == domain.FileRenamed {
		n.PreviousName = change.OldPath
		n.Moved = true
	}
	fd := domain.FileDiff{OldPath: change.OldPath, NewPath: change.NewPath, Binary: change.Binary}
	for i, h := range change.Hunks {
		if i > 0 {
			fd.Lines = append(fd.Lines, domain.DiffLine{Kind: domain.LineGap})
		}
		fd.Lines = append(fd.Lines, h.Lines...)
	}
	n.Diff = []domain.FileDiff{fd}
	domain.RollUpStats(n)
	switch {
	case why.text != "":
		n.Summary = domain.Summary{Origin: domain.OriginStructural, Text: why.text}
	case why.uncertainty != "":
		n.Summary = domain.Summary{Origin: domain.OriginNone, Uncertainty: why.uncertainty, Confidence: domain.ConfidenceLow}
	default:
		n.Summary = domain.Summary{Origin: domain.OriginNone}
	}
	return n
}

func opForStatus(s domain.FileStatus) domain.ChangeOp {
	switch s {
	case domain.FileAdded:
		return domain.OpAdd
	case domain.FileDeleted:
		return domain.OpRemove
	}
	return domain.OpChange
}

func sideForStatus(s domain.FileStatus) string {
	if s == domain.FileDeleted {
		return "base"
	}
	return "head"
}

func isGeneratedPath(p string) bool {
	if strings.HasPrefix(p, "vendor/") || strings.Contains(p, "/vendor/") ||
		strings.HasPrefix(p, "node_modules/") || strings.Contains(p, "/node_modules/") {
		return true
	}
	switch path.Base(p) {
	case "go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock", "poetry.lock", "uv.lock", "Gemfile.lock", "composer.lock":
		return true
	}
	return strings.HasSuffix(p, ".min.js") || strings.HasSuffix(p, ".min.css")
}
