package webassets

import "embed"

//go:embed all:dist
var FS embed.FS
