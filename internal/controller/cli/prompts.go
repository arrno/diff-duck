package cli

import (
	"io/fs"

	"github.com/arrno/diff-duck/prompts"
)

func subFS() (fs.FS, error) { return fs.Sub(prompts.Default, "default") }
