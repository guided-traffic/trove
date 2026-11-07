package postgres

import "github.com/guided-traffic/trove/internal/backup/tables"

// Registration of the table of 00047_user_avatars (R-API-055, R-BAK-004, Q-STOR-167):
//
//   - user_avatars: data. The rows name the avatar of a user, and the blobs they reference travel with them like the blobs of
//     any other reference; a restore brings the avatars back.
func init() {
	tables.Register("user_avatars", tables.Data, tables.Options{})
}
