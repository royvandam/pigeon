// Package web embeds Pigeon's templates and static assets.
package web

import "embed"

// Tailwind CSS is generated from the classes used in templates and scripts, so
// pages load no third-party code at runtime. CI checks that it is up to date.
//go:generate ../scripts/tailwindcss.sh --input tailwind.input.css --output static/tailwind.css --minify

// Assets contains the templates and browser assets included in the executable.
//
//go:embed templates/*.html static/*.js static/*.css static/*.webp static/fontawesome
var Assets embed.FS
