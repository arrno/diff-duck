package cli

import (
	"io/fs"

	"duck-diff/prompts"
)

func subFS() (fs.FS, error) { return fs.Sub(prompts.Default, "default") }
