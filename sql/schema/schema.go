// Package schema holds the goose migrations for the roster database.
package schema

import "embed"

// FS embeds all SQL migration files in this directory.
//
//go:embed *.sql
var FS embed.FS
