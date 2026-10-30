package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// SystemFacts are the three keys the first migration writes to system_state
// (R-OPS-024): the installation, the Valkey keyspace and the incarnation.
type SystemFacts struct {
	InstallationID string
	KeyspaceID     string
	Incarnation    string
}

// SystemFacts reads installation_id, keyspace_id and incarnation on the request
// pool, through Read. A missing key is an error: the first migration creates all three.
func (db *DB) SystemFacts(ctx context.Context) (SystemFacts, error) {
	var f SystemFacts
	err := db.Read(ctx, func(c q.DBTX) error {
		f = SystemFacts{}
		qs := q.New(c)
		for key, dst := range map[string]*string{
			"installation_id": &f.InstallationID, "keyspace_id": &f.KeyspaceID, "incarnation": &f.Incarnation,
		} {
			row, err := qs.GetSystemState(ctx, key)
			if err != nil {
				return fmt.Errorf("system_state %s: %w", key, err)
			}
			if err := json.Unmarshal(row.Value, dst); err != nil {
				return fmt.Errorf("system_state %s is not a JSON string: %w", key, err)
			}
		}
		return nil
	})
	if err != nil {
		return SystemFacts{}, err
	}
	return f, nil
}

// ServerVersion returns the PostgreSQL server version string (for example "18.6").
func (db *DB) ServerVersion(ctx context.Context) (string, error) {
	var version string
	err := db.Read(ctx, func(c q.DBTX) error {
		info, err := q.New(c).ServerInfo(ctx)
		version = info.Version
		return err
	})
	return version, err
}

// PoolerMode returns database.pooler.mode.
func (db *DB) PoolerMode() string { return db.cfg.Pooler.Mode }
