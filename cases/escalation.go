package cases

import (
	"context"
	"strings"
	"time"
)

// Escalate atomically changes routing and lifecycle under the existing case
// policy and expected version. Timer ownership alone grants no case authority.
func (s Service) Escalate(ctx context.Context, p Principal, id string, v uint64, candidates []Candidate, reason string, at time.Time) (Case, error) {
	if strings.TrimSpace(reason) == "" {
		return Case{}, ErrInvalid
	}
	return s.mutate(ctx, p, id, v, at, reason, "escalation", func(c *Case) error {
		if !canManage(p, *c) {
			return ErrDenied
		}
		if c.State == Draft || c.State == Resolved || c.State == Closed {
			return ErrInvalidTransition
		}
		route, e := Route(*c, candidates)
		if e != nil {
			return e
		}
		c.Assignments = []Assignment{route}
		c.State = Escalated
		return nil
	})
}
