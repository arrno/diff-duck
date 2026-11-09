package treesitter

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func runtimeWith(t *testing.T, l limits) *Runtime {
	t.Helper()
	rt, err := newRuntime(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	return rt
}

func TestParseRefusesOversizedFiles(t *testing.T) {
	l := defaultLimits
	l.maxSourceBytes = 100
	rt := runtimeWith(t, l)
	_, err := rt.Parse(context.Background(), pythonGrammar, []byte(strings.Repeat("x = 1\n", 20)))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestDeepNestingIsAParseErrorAndTheRuntimeRecovers(t *testing.T) {
	rt := newTestRuntime(t)
	deep := "x = " + strings.Repeat("(", 5000) + "1" + strings.Repeat(")", 5000) + "\n"
	_, err := NewPython(rt).Analyze(context.Background(), "m.py", []byte(deep))
	if err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatalf("a tree deeper than the cap must not be analyzed, got %v", err)
	}
	// The failed instance is discarded; the grammar keeps working.
	a := analyzePy(t, rt, "m.py", "def f():\n    pass\n")
	if len(a.Units) != 2 {
		t.Fatalf("after a failure: %+v", a.Units)
	}
}

func bigPython(n int) []byte {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		b.WriteString("def f(a, b):\n    if a:\n        return [x * 2 for x in range(b) if x % 3]\n    return {'k': (a, b)}\n\n")
	}
	return []byte(b.String())
}

func TestTimeoutAbandonsTheParse(t *testing.T) {
	l := defaultLimits
	l.timeout = time.Millisecond
	rt := runtimeWith(t, l)
	_, err := rt.Parse(context.Background(), pythonGrammar, bigPython(1<<20))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want a deadline error, got %v", err)
	}
}

func TestCancellationAbandonsTheParseAndTheRuntimeRecovers(t *testing.T) {
	rt := newTestRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	_, err := NewPython(rt).Analyze(ctx, "m.py", bigPython(1<<20))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want a cancellation error, got %v", err)
	}
	analyzePy(t, rt, "m.py", "x = 1\n")
}

func TestInstancesAreReusedButLargeOnesRecycled(t *testing.T) {
	l := defaultLimits
	l.recycleBytes = 1000
	rt := runtimeWith(t, l)
	ctx := context.Background()
	if _, err := rt.Parse(ctx, pythonGrammar, []byte("x = 1\n")); err != nil {
		t.Fatal(err)
	}
	lang := rt.languages["python"]
	first := <-lang.idle
	lang.idle <- first
	if _, err := rt.Parse(ctx, pythonGrammar, []byte("y = 2\n")); err != nil {
		t.Fatal(err)
	}
	if again := <-lang.idle; again != first {
		t.Fatal("a small parse should reuse the idle instance")
	} else {
		lang.idle <- again
	}
	if _, err := rt.Parse(ctx, pythonGrammar, bigPython(2000)); err != nil {
		t.Fatal(err)
	}
	if len(lang.idle) != 0 || !first.mod.IsClosed() {
		t.Fatal("an instance that parsed a large file must be discarded")
	}
}

func TestConcurrentParsesAgree(t *testing.T) {
	rt := newTestRuntime(t)
	want := analyzePy(t, rt, "util.py", sample)
	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := NewPython(rt).Analyze(context.Background(), "util.py", []byte(sample))
			if err != nil {
				errs <- err.Error()
				return
			}
			for j := range got.Units {
				if got.Units[j].Hash != want.Units[j].Hash || got.Units[j].StartLine != want.Units[j].StartLine {
					errs <- "units differ under concurrency"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestClosedRuntimeRefusesWork(t *testing.T) {
	rt, err := NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = rt.Close(context.Background())
	if _, err := rt.Parse(context.Background(), pythonGrammar, []byte("x = 1\n")); err == nil {
		t.Fatal("a closed runtime must refuse to parse")
	}
}

// record encodes one dd_node as shim.c lays it out.
func rawRecord(symbol, parent, startByte, endByte, startRow, endRow uint32, field uint16, flags uint8) []byte {
	b := make([]byte, recordSize)
	le := binary.LittleEndian
	le.PutUint32(b[0:], symbol)
	le.PutUint32(b[4:], parent)
	le.PutUint32(b[8:], startByte)
	le.PutUint32(b[12:], endByte)
	le.PutUint32(b[16:], startRow)
	le.PutUint32(b[24:], endRow)
	le.PutUint16(b[32:], field)
	b[34] = 1
	b[35] = flags
	return b
}

func TestDecodeRejectsMalformedRecords(t *testing.T) {
	src := []byte("abc")
	symbols, fields := []string{"end", "module", "identifier"}, []string{"", "name"}
	root := rawRecord(1, noParent, 0, 3, 0, 0, 0, 0)
	cases := map[string][]byte{
		"unknown symbol":    append(append([]byte{}, root...), rawRecord(9, 0, 0, 1, 0, 0, 0, 0)...),
		"unknown field":     append(append([]byte{}, root...), rawRecord(2, 0, 0, 1, 0, 0, 7, 0)...),
		"forward parent":    append(append([]byte{}, root...), rawRecord(2, 1, 0, 1, 0, 0, 0, 0)...),
		"range past source": append(append([]byte{}, root...), rawRecord(2, 0, 0, 9, 0, 0, 0, 0)...),
		"inverted range":    append(append([]byte{}, root...), rawRecord(2, 0, 2, 1, 0, 0, 0, 0)...),
		"inverted rows":     append(append([]byte{}, root...), rawRecord(2, 0, 0, 1, 3, 1, 0, 0)...),
		"root with parent":  rawRecord(1, 0, 0, 3, 0, 0, 0, 0),
	}
	for name, raw := range cases {
		if _, err := decode(raw, uint64(len(raw)/recordSize), src, symbols, fields); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := decode(root, 2, src, symbols, fields); err == nil {
		t.Error("a count beyond the data was accepted")
	}
	// The ERROR symbol sits outside the grammar's table and marks the tree as erroneous.
	raw := append(append([]byte{}, root...), rawRecord(symbolError, 0, 0, 1, 0, 0, 1, 0)...)
	tree, err := decode(raw, 2, src, symbols, fields)
	if err != nil {
		t.Fatal(err)
	}
	child := tree.Root().Children()[0]
	if child.Type() != "ERROR" || !child.IsError() || child.Field() != "name" || child.Text() != "a" {
		t.Errorf("error node decoded as %q error=%v field=%q", child.Type(), child.IsError(), child.Field())
	}
}

// Close may run while parses, even the first one that compiles the grammar, are in flight.
// They fail or finish; the process does not crash.
func TestCloseDuringParsesIsSafe(t *testing.T) {
	for round := 0; round < 20; round++ {
		rt, err := NewRuntime(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = NewPython(rt).Analyze(context.Background(), "m.py", []byte(sample))
			}()
		}
		time.Sleep(time.Duration(round) * time.Millisecond)
		if err := rt.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		if _, err := rt.Parse(context.Background(), pythonGrammar, []byte("x = 1\n")); err == nil {
			t.Fatal("parsing after Close must fail")
		}
	}
}

func TestInstancesWhoseMemoryGrewAreRecycled(t *testing.T) {
	l := defaultLimits
	l.recycleMemory = 1 // below any instance's memory, so every one counts as grown
	rt := runtimeWith(t, l)
	if _, err := rt.Parse(context.Background(), pythonGrammar, []byte("x = 1\n")); err != nil {
		t.Fatal(err)
	}
	if n := len(rt.languages["python"].idle); n != 0 {
		t.Fatalf("an instance whose memory grew past the limit must be discarded, %d idle", n)
	}
}

func TestNameTablesAreReadWithinBounds(t *testing.T) {
	rt := newTestRuntime(t)
	lang, err := rt.language(context.Background(), pythonGrammar)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := rt.acquire(context.Background(), lang)
	if err != nil {
		t.Fatal(err)
	}
	mem := inst.mod.Memory()
	end := mem.Size()
	if !mem.Write(end-4, []byte("abcd")) {
		t.Fatal("write")
	}
	if _, err := cString(mem, end-4); err == nil {
		t.Error("a name running to the end of memory without a terminator must be refused")
	}
	if s, err := cString(mem, end+100); err == nil {
		t.Errorf("a pointer past memory must be refused, got %q", s)
	}
	if !mem.Write(end-4, []byte("ab\x00d")) {
		t.Fatal("write")
	}
	if s, err := cString(mem, end-4); err != nil || s != "ab" {
		t.Errorf("terminated name: %q %v", s, err)
	}
}
