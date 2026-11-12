package principal

import (
	"context"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The users and groups of the search field's suggestions (R-API-060 to R-API-062): one statement each, ranked in the statement
// and cut at limit + 1, for a holder of identities:read, which the API checks before it asks (Q-API-272).

// UserSuggestion is a user as a suggestion names it.
type UserSuggestion struct {
	ID, Username, DisplayName string
	Disabled                  bool
}

// GroupSuggestion is a group as a suggestion names it.
type GroupSuggestion struct {
	Name, Description string
}

// SuggestUsers returns at most limit users of which every term is part of the lower-cased username or display name, best
// first, and whether another one matches. text is the lower-cased text, terms its parts between white space.
func (m *Manager) SuggestUsers(ctx context.Context, text string, terms []string, limit int) ([]UserSuggestion, bool, error) {
	var out []UserSuggestion
	err := m.db.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).SearchUsers(ctx, q.SearchUsersParams{Terms: terms, Text: text, MaxRows: int32(min(limit, 100) + 1)}) //nolint:gosec // bounded
		if err != nil {
			return err
		}
		out = make([]UserSuggestion, 0, len(rows))
		for _, r := range rows {
			out = append(out, UserSuggestion{ID: r.ID, Username: r.Username, DisplayName: r.DisplayName.String, Disabled: r.Disabled})
		}
		return nil
	})
	if err != nil {
		return nil, false, DBErr("principal.SuggestUsers", err)
	}
	return out[:min(limit, len(out))], len(out) > limit, nil
}

// SuggestGroups returns at most limit groups of which every term is part of the lower-cased name, best first, and whether
// another one matches.
func (m *Manager) SuggestGroups(ctx context.Context, text string, terms []string, limit int) ([]GroupSuggestion, bool, error) {
	var out []GroupSuggestion
	err := m.db.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).SearchGroups(ctx, q.SearchGroupsParams{Terms: terms, Text: text, MaxRows: int32(min(limit, 100) + 1)}) //nolint:gosec // bounded
		if err != nil {
			return err
		}
		out = make([]GroupSuggestion, 0, len(rows))
		for _, r := range rows {
			out = append(out, GroupSuggestion{Name: r.Name, Description: r.Description})
		}
		return nil
	})
	if err != nil {
		return nil, false, DBErr("principal.SuggestGroups", err)
	}
	return out[:min(limit, len(out))], len(out) > limit, nil
}
