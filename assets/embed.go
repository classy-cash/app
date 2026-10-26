// Package assets embeds the brand kit, icons and bundled fonts served at /assets/.
package assets

import "embed"

// FS contains brand/, images/, fonts/ and the web manifest.
//
//go:embed brand/*.svg brand/*.png brand/tokens.css brand/site.webmanifest images/* fonts/*.woff2 site.webmanifest
var FS embed.FS
