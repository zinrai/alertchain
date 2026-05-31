package ui

import "embed"

//go:embed templates/*.html
var uiTemplatesFS embed.FS

//go:embed static/htmx.min.js static/style.css static/time-sync.js
var uiStaticFS embed.FS
