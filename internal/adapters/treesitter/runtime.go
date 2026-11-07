// Package treesitter extracts code units with tree-sitter grammars that run as WebAssembly in
// wazero, a pure-Go runtime, so the build stays cgo-free.
//
// Each language is one embedded module (tree-sitter core + that grammar + shim/shim.c), built
// by scripts/build-treesitter-wasm.sh. A module is compiled the first time its language is
// parsed, then reused. Instances of a module are pooled: an instance runs one parse at a time,
// and one that parsed a large file, failed, or was cancelled is discarded rather than reused.
//
// Adding a language is a contained change:
//
//  1. scripts/treesitter/<lang>.env pins the grammar's Go module, version, source directory,
//     and entry symbol.
//  2. `make treesitter-wasm GRAMMAR=<lang>` builds wasm/<lang>.wasm and its licence text.
//  3. <lang>.go embeds the module as a Grammar and implements ports.SourceAnalyzer, using Tree,
//     Node, and fingerprint; <lang>_test.go covers it.
//  4. The controller registers the analyzer.
package treesitter

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Grammar is one language's embedded WASM module.
type Grammar struct {
	name string
	wasm []byte
}

// ErrTooLarge means a file is bigger than the runtime will parse.
var ErrTooLarge = errors.New("file is too large to analyze")

var errClosed = errors.New("tree-sitter runtime is closed")

// ErrSyntax means the file does not parse cleanly.
var ErrSyntax = errors.New("syntax error")

// limits bound the work and memory one parse can take.
type limits struct {
	maxSourceBytes int           // larger files are refused
	timeout        time.Duration // per parse, on top of the caller's context
	recycleBytes   int           // an instance that parsed a larger file is discarded
	recycleMemory  uint32        // an instance whose memory grew past this is discarded
	maxIdle        int           // instances kept per language
}

var defaultLimits = limits{
	maxSourceBytes: 2 << 20,
	timeout:        30 * time.Second,
	recycleBytes:   256 << 10,
	recycleMemory:  32 << 20,
	maxIdle:        runtime.GOMAXPROCS(0),
}

// memoryLimitPages caps one instance's linear memory (64 KiB pages): 256 MiB.
const memoryLimitPages = 4096

// Runtime runs grammars. It is safe for concurrent use. Close releases everything it holds.
type Runtime struct {
	wz     wazero.Runtime
	limits limits

	// inflight is held for reading by every parse and for writing by Close, which waits for
	// parses to stop before releasing wazero. closing stops parses that are still running.
	inflight sync.RWMutex
	closing  context.Context
	stopAll  context.CancelFunc

	mu        sync.Mutex
	closed    bool
	languages map[string]*language
}

// language is a compiled grammar and its idle instances.
type language struct {
	once     sync.Once
	err      error
	compiled wazero.CompiledModule
	symbols  []string // node type names by symbol id
	fields   []string // field names by field id; 0 is no field
	idle     chan *instance
}

type instance struct {
	mod                                   api.Module
	alloc, free, parse, result, statusFor api.Function
}

// NewRuntime returns a runtime with no languages loaded yet.
func NewRuntime(ctx context.Context) (*Runtime, error) {
	return newRuntime(ctx, defaultLimits)
}

func newRuntime(ctx context.Context, l limits) (*Runtime, error) {
	// Cancellation goes through tree-sitter's progress callback (dd.should_stop) rather than
	// wazero's WithCloseOnContextDone, whose compiled-in checks triple parse time.
	cfg := wazero.NewRuntimeConfig().WithMemoryLimitPages(memoryLimitPages)
	wz := wazero.NewRuntimeWithConfig(ctx, cfg)
	// The modules import only clock, random, and stdio stubs; no filesystem is mounted.
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, wz); err != nil {
		_ = wz.Close(ctx)
		return nil, fmt.Errorf("start wasm runtime: %w", err)
	}
	_, err := wz.NewHostModuleBuilder("dd").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context) uint32 {
			if ctx.Err() != nil {
				return 1
			}
			return 0
		}).
		Export("should_stop").
		Instantiate(ctx)
	if err != nil {
		_ = wz.Close(ctx)
		return nil, fmt.Errorf("start wasm runtime: %w", err)
	}
	closing, stopAll := context.WithCancel(context.Background())
	return &Runtime{wz: wz, limits: l, closing: closing, stopAll: stopAll, languages: map[string]*language{}}, nil
}

// Close stops parses in flight, waits for them, and releases every compiled grammar and
// instance. Later parses fail.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stopAll()
	r.inflight.Lock()
	defer r.inflight.Unlock()
	return r.wz.Close(ctx)
}

func (r *Runtime) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Parse returns src's syntax tree. A tree with syntax errors is still returned; callers decide.
func (r *Runtime) Parse(ctx context.Context, g Grammar, src []byte) (*Tree, error) {
	if len(src) > r.limits.maxSourceBytes {
		return nil, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, len(src), r.limits.maxSourceBytes)
	}
	r.inflight.RLock()
	defer r.inflight.RUnlock()
	if r.isClosed() {
		return nil, errClosed
	}
	lang, err := r.language(ctx, g)
	if err != nil {
		return nil, err
	}
	inst, err := r.acquire(ctx, lang)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.limits.timeout)
	defer cancel()
	defer context.AfterFunc(r.closing, cancel)()
	tree, err := inst.run(ctx, lang, src)
	if err != nil {
		// A trap or cancellation can leave the instance closed or inconsistent.
		_ = inst.mod.Close(context.Background())
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("parse %s: %w", g.name, ctxErr)
		}
		return nil, fmt.Errorf("parse %s: %w", g.name, err)
	}
	r.release(lang, inst, len(src))
	return tree, nil
}

// language compiles a grammar on first use.
func (r *Runtime) language(ctx context.Context, g Grammar) (*language, error) {
	r.mu.Lock()
	lang := r.languages[g.name]
	if lang == nil {
		lang = &language{idle: make(chan *instance, max(1, r.limits.maxIdle))}
		r.languages[g.name] = lang
	}
	r.mu.Unlock()
	lang.once.Do(func() {
		// Compilation is shared by every later caller, so one caller's cancellation must not
		// poison it.
		compileCtx := context.WithoutCancel(ctx)
		lang.compiled, lang.err = r.wz.CompileModule(compileCtx, g.wasm)
		if lang.err != nil {
			lang.err = fmt.Errorf("compile %s grammar: %w", g.name, lang.err)
			return
		}
		inst, err := r.instantiate(compileCtx, lang)
		if err != nil {
			lang.err = err
			return
		}
		lang.symbols, lang.fields, lang.err = inst.names(compileCtx)
		if lang.err != nil {
			lang.err = fmt.Errorf("read %s grammar tables: %w", g.name, lang.err)
			_ = inst.mod.Close(compileCtx)
			return
		}
		lang.idle <- inst
	})
	return lang, lang.err
}

func (r *Runtime) acquire(ctx context.Context, lang *language) (*instance, error) {
	for {
		select {
		case inst := <-lang.idle:
			if inst.mod.IsClosed() {
				continue
			}
			return inst, nil
		default:
			return r.instantiate(ctx, lang)
		}
	}
}

// release returns an instance to the pool unless it has grown or the pool is full. WASM memory
// never shrinks, so an instance that parsed a large file would keep that memory forever.
func (r *Runtime) release(lang *language, inst *instance, parsed int) {
	if parsed > r.limits.recycleBytes || inst.mod.Memory().Size() > r.limits.recycleMemory {
		_ = inst.mod.Close(context.Background())
		return
	}
	select {
	case lang.idle <- inst:
	default:
		_ = inst.mod.Close(context.Background())
	}
}

func (r *Runtime) instantiate(ctx context.Context, lang *language) (*instance, error) {
	cfg := wazero.NewModuleConfig().
		WithName(""). // anonymous, so several instances of one grammar can coexist
		WithStartFunctions("_initialize")
	mod, err := r.wz.InstantiateModule(context.WithoutCancel(ctx), lang.compiled, cfg)
	if err != nil {
		return nil, fmt.Errorf("instantiate grammar: %w", err)
	}
	inst := &instance{
		mod:       mod,
		alloc:     mod.ExportedFunction("dd_alloc"),
		free:      mod.ExportedFunction("dd_free"),
		parse:     mod.ExportedFunction("dd_parse"),
		result:    mod.ExportedFunction("dd_result"),
		statusFor: mod.ExportedFunction("dd_status"),
	}
	if inst.alloc == nil || inst.free == nil || inst.parse == nil || inst.result == nil || inst.statusFor == nil {
		_ = mod.Close(ctx)
		return nil, errors.New("grammar module lacks the shim's exports")
	}
	return inst, nil
}

// shim status codes, from shim.c.
var statusText = map[uint64]string{
	1: "grammar is not compatible with the tree-sitter core",
	2: "parser returned no tree",
	3: "out of memory",
	4: "syntax nested too deeply",
	5: "parse stopped",
}

func (inst *instance) run(ctx context.Context, lang *language, src []byte) (*Tree, error) {
	// Only the parse can run long, and it alone needs ctx, for dd.should_stop. The other calls
	// must finish even after cancellation, so the instance stays consistent.
	quick := context.WithoutCancel(ctx)
	size := uint64(max(len(src), 1))
	res, err := inst.alloc.Call(quick, size)
	if err != nil {
		return nil, err
	}
	ptr := res[0]
	if ptr == 0 {
		return nil, errors.New("out of memory")
	}
	defer func() { _, _ = inst.free.Call(quick, ptr) }()
	if !inst.mod.Memory().Write(uint32(ptr), src) {
		return nil, errors.New("source does not fit in wasm memory")
	}
	res, err = inst.parse.Call(ctx, ptr, uint64(len(src)))
	if err != nil {
		return nil, err
	}
	count := res[0]
	if count == 0 {
		st, err := inst.statusFor.Call(quick)
		if err != nil {
			return nil, err
		}
		if text, ok := statusText[st[0]]; ok {
			return nil, errors.New(text)
		}
		return nil, fmt.Errorf("parse failed with status %d", st[0])
	}
	res, err = inst.result.Call(quick)
	if err != nil {
		return nil, err
	}
	raw, ok := inst.mod.Memory().Read(uint32(res[0]), uint32(min(count*recordSize, 1<<32-1)))
	if !ok || count*recordSize > uint64(len(raw)) {
		return nil, errors.New("parse result lies outside wasm memory")
	}
	return decode(raw, count, src, lang.symbols, lang.fields)
}

// names reads the grammar's node type and field names.
func (inst *instance) names(ctx context.Context) (symbols, fields []string, err error) {
	read := func(countFn, nameFn string, first uint64) ([]string, error) {
		count, name := inst.mod.ExportedFunction(countFn), inst.mod.ExportedFunction(nameFn)
		if count == nil || name == nil {
			return nil, fmt.Errorf("missing %s or %s", countFn, nameFn)
		}
		n, err := count.Call(ctx)
		if err != nil {
			return nil, err
		}
		if n[0] > 1<<16 {
			return nil, fmt.Errorf("implausible table size %d", n[0])
		}
		out := make([]string, n[0]+first)
		for i := first; i < n[0]+first; i++ {
			p, err := name.Call(ctx, i)
			if err != nil {
				return nil, err
			}
			if out[i], err = cString(inst.mod.Memory(), uint32(p[0])); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	if symbols, err = read("dd_symbol_count", "dd_symbol_name", 0); err != nil {
		return nil, nil, err
	}
	// Field ids start at 1; the count excludes the "no field" id 0.
	if fields, err = read("dd_field_count", "dd_field_name", 1); err != nil {
		return nil, nil, err
	}
	return symbols, fields, nil
}

const maxNameLength = 256

// cString reads a NUL-terminated string of bounded length.
func cString(mem api.Memory, p uint32) (string, error) {
	if p == 0 {
		return "", nil
	}
	n := min(uint32(maxNameLength), mem.Size()-min(p, mem.Size()))
	b, ok := mem.Read(p, n)
	if !ok {
		return "", errors.New("name lies outside wasm memory")
	}
	for i, c := range b {
		if c == 0 {
			return string(b[:i]), nil
		}
	}
	return "", errors.New("name is not terminated")
}
