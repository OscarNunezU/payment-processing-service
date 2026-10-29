// Package migrations holds the schema of each database, one folder per
// database. The migrate command embeds them.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed payments webhooks
var all embed.FS

// For returns the migrations of database.
func For(database string) (fs.FS, error) {
	if info, err := fs.Stat(all, database); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("no migrations for database %q", database)
	}
	return fs.Sub(all, database)
}
