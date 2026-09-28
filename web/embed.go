// Package web embeds the adcp-test UI static assets served by the local server.
package web

import "embed"

//go:embed index.html style.css app.js
var FS embed.FS
