package cli

import (
	"io/fs"

	"github.com/arrno/diffduck/prompts"
)

func subFS() (fs.FS, error) { return fs.Sub(prompts.Default, "default") }
