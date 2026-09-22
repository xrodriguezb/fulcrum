// Package migrations embeds the SQL migration set so that a binary carries the
// schema it expects. A deployment that cannot reach its migration files is a
// deployment that starts against an unknown schema.
package migrations

import "embed"

// FS holds every migration file, named <version>_<name>.<up|down>.sql.
//
//go:embed *.sql
var FS embed.FS
