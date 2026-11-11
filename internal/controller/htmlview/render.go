// Package htmlview renders a compressed diff as one self-contained HTML page. The page is built
// from clients/viewer with `make viewer` and committed, so `go build` needs no Node toolchain.
package htmlview

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/arrno/diffduck/internal/domain"
)

//go:embed viewer.html
var page string

const (
	modelPlaceholder   = "__DIFFDUCK_MODEL__"
	versionPlaceholder = "__DIFFDUCK_VERSION__"
)

// Render returns the viewer page holding the compressed diff. Go's JSON encoder escapes "<",
// so repository content cannot close the script tag it is embedded in. The page is split on
// the placeholder rather than searched after insertion, so repository content stays inert.
// version is the diffduck version the page names in its footer.
func Render(result *domain.CompressedDiff, version string) ([]byte, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode compressed diff: %w", err)
	}
	return fill(data, version)
}

// RenderApp returns the viewer page holding no diff, which makes it the interactive application
// that loads diffs from the local server.
func RenderApp(version string) ([]byte, error) {
	return fill([]byte("null"), version)
}

func fill(data []byte, version string) ([]byte, error) {
	if strings.Count(page, modelPlaceholder) != 1 || strings.Count(page, versionPlaceholder) != 1 {
		return nil, fmt.Errorf("the embedded viewer must hold exactly one model and one version placeholder; rebuild it with make viewer")
	}
	// The version goes in first, so nothing in the model is ever searched.
	withVersion := strings.Replace(page, versionPlaceholder, html.EscapeString(version), 1)
	before, after, _ := strings.Cut(withVersion, modelPlaceholder)
	return []byte(before + string(data) + after), nil
}
