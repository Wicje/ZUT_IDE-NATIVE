package agents

import (
	"context"

	"mayoi/internal/opencode"
)

// opencodeSessions lists the sessions of the local opencode server.
// OpenCode keeps its sessions in SQLite; the server knows them, so they
// come off it rather than out of files. A server that is not running, or
// one the context gives up on, lists nothing.
func opencodeSessions(ctx context.Context) ([]Session, error) {
	ss, err := opencode.New().Sessions(ctx, 200)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(ss))
	for _, s := range ss {
		out = append(out, Session{
			Harness: Opencode,
			ID:      s.ID,
			Dir:     s.Dir,
			Title:   s.Title,
			Updated: s.Updated,
		})
	}
	return out, nil
}
