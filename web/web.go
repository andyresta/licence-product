// Package web embeds the admin panel's HTML templates into the compiled binary —
// go:embed cannot reach outside its own package directory, hence this thin package
// existing solely to own the directive (same pattern FixUnit itself uses).
package web

import "embed"

//go:embed templates
var FS embed.FS
