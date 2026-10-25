// Package db is the service's database handle.
package db

import "context"

// DB is a database connection.
type DB struct{}

// Ping checks the connection.
func (*DB) Ping(ctx context.Context) error { return ctx.Err() }
