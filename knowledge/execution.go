package knowledge

import (
	"context"
	"database/sql"
	"encoding/hex"
)

// ExecutionBinding is trusted host lifecycle identity, separate from document
// metadata and worker evidence. A restored/reactivated host must not adopt old work.
type ExecutionBinding struct {
	Generation int64  `json:"generation"`
	Runtime    string `json:"runtime"`
}
type ExecutionFence func(context.Context, *sql.Tx, ProcessingJob, ExecutionBinding) (ExecutionBinding, error)

func validExecutionBinding(b ExecutionBinding) bool {
	v, e := hex.DecodeString(b.Runtime)
	return b.Generation > 0 && e == nil && len(v) == 32
}
func (q *ProcessingQueue) SetExecutionFence(f ExecutionFence) { q.execution = f }
func (q *ProcessingQueue) checkExecution(ctx context.Context, tx *sql.Tx, j *ProcessingJob, intake bool) error {
	if q.execution == nil {
		return nil
	} // generic legacy host; fenced hosts always supply this port
	if !intake && !validExecutionBinding(j.Execution) {
		return ErrDenied
	}
	current, e := q.execution(ctx, tx, *j, j.Execution)
	if e != nil {
		return e
	}
	if !validExecutionBinding(current) || (!intake && current != j.Execution) {
		return ErrDenied
	}
	j.Execution = current
	return nil
}
