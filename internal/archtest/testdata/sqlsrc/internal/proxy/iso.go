package proxy

import "github.com/jackc/pgx/v5"

// Opts asks for a non-default isolation level through the pgx API.
var Opts = pgx.TxOptions{IsoLevel: pgx.RepeatableRead}
