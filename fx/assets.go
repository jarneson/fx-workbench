package fxassets

import (
	"embed"
	"io/fs"
)

// Files contains every Workbench-owned asset installed into an fx config root.
//
//go:embed all:skills all:principles all:agent-profiles
var embedded embed.FS

func Files() fs.FS { return embedded }
