//go:build verify

package cli

import (
	"fmt"
	"strings"

	"github.com/arrno/diff-duck/internal/adapters/scriptedmodel"
	"github.com/arrno/diff-duck/internal/services/ports"
)

// verificationModel provides the scripted model to verification builds.
func verificationModel(adapter string) (ports.LanguageModel, error) {
	behavior, ok := strings.CutPrefix(adapter, "scripted:")
	if !ok {
		return nil, fmt.Errorf("unknown adapter %q: use claude-cli, anthropic-api, or scripted:<behavior>", adapter)
	}
	return scriptedmodel.New(scriptedmodel.Behavior(behavior))
}
