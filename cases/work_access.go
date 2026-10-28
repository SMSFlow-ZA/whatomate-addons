package cases

// operatorConflict distinguishes an employee's own complaint from operator
// duties. Verified historical aliases remain conflicts. A participant's
// supervisor is not automatically a complainant or subject.
func operatorConflict(p Principal, c Case) bool {
	return subject(p, c) || complainant(p, c)
}

// operatorRead grants the full internal view, never an employee's own status.
func operatorRead(p Principal, c Case) bool {
	return !operatorConflict(p, c) && ((assigned(p, c) && scoped(p, c, WorkAssigned)) || (len(c.Assignments) == 0 && scoped(p, c, ReadTriage)))
}

// CanWork is the shared policy for case-bound assistance and background actions.
// Triage visibility and employee status access do not imply assigned work access.
func CanWork(p Principal, c Case) bool {
	return !operatorConflict(p, c) && assigned(p, c) && scoped(p, c, WorkAssigned)
}
