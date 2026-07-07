package migrations

import "embed"

// FS contains numbered SQL migrations.
//
//go:embed *.sql
var FS embed.FS
