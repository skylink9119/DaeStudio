package daednext

import "embed"

// WebFS embeds the standalone modern web dashboard into a single Go binary.
//
//go:embed web
var WebFS embed.FS
