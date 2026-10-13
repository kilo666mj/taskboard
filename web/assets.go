package webassets

import "embed"

// Files contains the installable web client served by Taskboard.
//
//go:embed index.html app.css app.js theme.js manifest.webmanifest sw.js icons/*
var Files embed.FS
