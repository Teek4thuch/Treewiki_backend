package migrations

import "embed"

// Files is the schema authority, embedded in the operator CLI.
//go:embed *.sql
var Files embed.FS
