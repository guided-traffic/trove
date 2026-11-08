package postgres

import "github.com/guided-traffic/trove/internal/backup/tables"

// Registrations of the tables of 00054_upload_sessions (R-STOR-011, R-BAK-004).
//
//   - upload_sessions, upload_session_parts: transient. The staged bytes of a session are objects below _uploads/ that a backup
//     does not carry (R-STOR-047), so a restored row would name a multipart upload that no longer exists; a client restarts its
//     upload after a restore.
func init() {
	tables.Register("upload_sessions", tables.Transient, tables.Options{})
	tables.Register("upload_session_parts", tables.Transient, tables.Options{})
}
