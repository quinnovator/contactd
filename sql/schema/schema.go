// Package schema embeds the SQL migrations. Files are applied in name order;
// the numeric prefix of each is the PRAGMA user_version it brings the
// database to.
package schema

import "embed"

// FS holds every NNNN_name.sql migration.
//
//go:embed *.sql
var FS embed.FS
