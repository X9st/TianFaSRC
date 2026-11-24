package webui

import "embed"

// Dist contains the production Next.js static export. The checked-in index is a
// small development fallback; make web replaces it with the full export.
//
// The all: prefix is required because Next.js intentionally emits _next and
// _not-found paths, which Go's default embed traversal would otherwise skip.
//
//go:embed all:dist
var Dist embed.FS
