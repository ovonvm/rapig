// Package migrations embeds the ordered SQL schema migrations.
//
// Files must be named NNNN_description.sql and are applied in lexical order
// exactly once, recorded in schema_migrations.
package migrations

import "embed"

// FS holds the ordered *.sql migration files.
//
//go:embed *.sql
var FS embed.FS
