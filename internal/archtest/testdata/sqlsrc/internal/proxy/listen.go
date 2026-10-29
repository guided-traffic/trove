package proxy

import "context"

// Conn stands in for a pgx connection.
type Conn interface{ WaitForNotification(context.Context) error }

// Listen subscribes to notifications, which trove never does.
const Listen = "LISTEN trove_changes"

// Wait waits for one.
func Wait(ctx context.Context, c Conn) error { return c.WaitForNotification(ctx) }

// Notify publishes through the database.
const Notify = "SELECT pg_notify('trove', 'x')"
