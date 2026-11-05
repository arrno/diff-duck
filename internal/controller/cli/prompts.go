package cli

import (
	"io/fs"

	"github.com/arrno/duck-diff/prompts"
)

func subFS() (fs.FS, error) { return fs.Sub(prompts.Default, "default") }
