package knowledge

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestExecutionBindingRejectsForeignOrUnboundCompletion(t *testing.T) {
	good := ExecutionBinding{Generation: 1, Runtime: strings.Repeat("a", 64)}
	for _, bad := range []ExecutionBinding{{}, {Generation: 0, Runtime: good.Runtime}, {Generation: 1, Runtime: "bad"}, {Generation: 2, Runtime: good.Runtime}, {Generation: 1, Runtime: strings.Repeat("b", 64)}} {
		q := &ProcessingQueue{execution: func(context.Context, *sql.Tx, ProcessingJob, ExecutionBinding) (ExecutionBinding, error) {
			return bad, nil
		}}
		job := ProcessingJob{Execution: good}
		if e := q.checkExecution(t.Context(), nil, &job, false); e == nil {
			t.Fatal("foreign binding accepted", bad)
		}
	}
	q := &ProcessingQueue{execution: func(context.Context, *sql.Tx, ProcessingJob, ExecutionBinding) (ExecutionBinding, error) {
		return good, nil
	}}
	job := ProcessingJob{}
	if e := q.checkExecution(t.Context(), nil, &job, false); e == nil {
		t.Fatal("legacy unbound job adopted as completion")
	}
	if e := q.checkExecution(t.Context(), nil, &job, true); e != nil || job.Execution != good {
		t.Fatal("trusted intake failed", e)
	}
}
