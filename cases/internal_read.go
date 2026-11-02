package cases

// CanReadInternal grants the full staff view, excluding own-status access and
// every current or historical subject/complainant identity conflict.
func CanReadInternal(p Principal, c Case) bool { return operatorRead(p, c) }
