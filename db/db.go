// Package db embeds SQL schema migrations into the binary.
package db

import "embed"

// Migrations holds the goose SQL migration files.
//
//go:embed migrations/*.sql
var Migrations embed.FS
