// Package htmlview renders a compressed diff as one self-contained HTML page. The page is built
// from clients/viewer with `make viewer` and committed, so `go build` needs no Node toolchain.
package htmlview

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/arrno/diff-duck/internal/domain"
)

//go:embed viewer.html
var page string

const modelPlaceholder = "__DIFF_DUCK_MODEL__"

// Render returns the viewer page holding the compressed diff. Go's JSON encoder escapes "<",
// so repository content cannot close the script tag it is embedded in. The page is split on
// the placeholder rather than searched after insertion, so repository content stays inert.
func Render(result *domain.CompressedDiff) ([]byte, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode compressed diff: %w", err)
	}
	return fill(data)
}

// RenderApp returns the viewer page holding no diff, which makes it the interactive application
// that loads diffs from the local server.
func RenderApp() ([]byte, error) {
	return fill([]byte("null"))
}

func fill(data []byte) ([]byte, error) {
	if strings.Count(page, modelPlaceholder) != 1 {
		return nil, fmt.Errorf("the embedded viewer must hold exactly one model placeholder; rebuild it with make viewer")
	}
	before, after, _ := strings.Cut(page, modelPlaceholder)
	return []byte(before + string(data) + after), nil
}
