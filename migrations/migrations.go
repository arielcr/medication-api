// Package migrations embeds the SQL migration files into the binary.
//
// Thanks to go:embed the service ships as a single executable: there is
// no need to copy .sql files next to it in the Docker image.
package migrations

import "embed"

// FS contains every *.sql migration in this directory. Files are named
// NNNNN_description.sql and applied in version order by goose.
//
//go:embed *.sql
var FS embed.FS
