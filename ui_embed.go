package main

import "embed"

//go:embed ui/templates/*.html
var uiTemplatesFS embed.FS

//go:embed ui/static/htmx.min.js ui/static/style.css ui/static/time-sync.js
var uiStaticFS embed.FS
