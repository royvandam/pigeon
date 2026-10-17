// Package web embeds the uploader's templates and static assets.
package web

import "embed"

// Assets contains the templates and browser assets included in the executable.
//
//go:embed templates/*.html static/*.js static/*.css
var Assets embed.FS
