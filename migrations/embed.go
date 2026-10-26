// Package migrations embeds the versioned SQL schema files.
package migrations

import "embed"

// FS holds every NNNN_name.sql migration in application order.
//
//go:embed *.sql
var FS embed.FS
