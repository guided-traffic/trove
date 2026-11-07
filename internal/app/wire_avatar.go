package app

import (
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/avatar"
)

// avatarService is the avatar store of the management API (R-API-055): the PNG in the blob store, its name in user_avatars.
// One store serves every endpoint of the replica, so the bound on the images decoded at one time (avatar.MaxConcurrent) is
// the replica's, not the endpoint's. A replica without infrastructure (the shell profile) has none, and the operations
// answer 503.
func (a *App) avatarService() v1.AvatarService {
	in := part[infra](a.parts)
	if in.db == nil || in.blobs == nil || in.rec == nil {
		return nil
	}
	return avatar.NewStore(in.db, in.blobs, in.rec, avatar.NewProcessor())
}
