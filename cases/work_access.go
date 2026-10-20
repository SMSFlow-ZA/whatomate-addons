package cases

// CanWork is the shared policy for case-bound assistance and background actions.
// Triage visibility and employee status access do not imply assigned work access.
func CanWork(p Principal, c Case) bool {
	return !subject(p, c) && assigned(p, c) && scoped(p, c, WorkAssigned)
}
