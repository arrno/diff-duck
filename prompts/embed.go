// Package prompts holds the built-in prompt variant, so the binary works from any directory.
package prompts

import "embed"

// Default holds the built-in variant under default/.
//
//go:embed default/*.md default/reading-levels/*.md
var Default embed.FS
