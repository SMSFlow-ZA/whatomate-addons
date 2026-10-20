package governance

import "context"

type subjectKey struct{}

// SubjectIDs returns only canonical and trusted server-resolved historical IDs.
// It supplies no grants and never changes the current operator's own identity.
func SubjectIDs(ctx context.Context, canonical string) []string {
	out := []string{canonical}
	if ids, ok := ctx.Value(subjectKey{}).([]string); ok {
		for _, id := range ids {
			if id != "" && id != canonical {
				out = append(out, id)
			}
		}
	}
	return out
}
func SubjectMatches(ctx context.Context, canonical, actual string) bool {
	for _, id := range SubjectIDs(ctx, canonical) {
		if actual == id {
			return true
		}
	}
	return false
}
func (s PhysicalService) subjectContext(ctx context.Context, org, subject string) (context.Context, error) {
	if s.SubjectAliases == nil {
		return ctx, nil
	}
	ids, e := s.SubjectAliases(ctx, org, subject)
	if e != nil {
		return ctx, e
	}
	return context.WithValue(ctx, subjectKey{}, append([]string(nil), ids...)), nil
}
