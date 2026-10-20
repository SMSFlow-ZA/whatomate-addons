package documents

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

type osExecutor struct{}
type boundedBuffer struct {
	mu       sync.Mutex
	data     []byte
	limit    int64
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if int64(len(b.data))+int64(len(p)) > b.limit {
		b.exceeded = true
		return 0, errors.New("tool output limit exceeded")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func (osExecutor) run(ctx context.Context, program string, args []string, stdin io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, ErrQuarantined
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.WaitDelay = time.Second
	cmd.Stdin = stdin
	cmd.Env = []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "OMP_THREAD_LIMIT=1", "HOME=/nonexistent", "TMPDIR=/tmp"}
	output := &boundedBuffer{limit: limit}
	diagnostics := &boundedBuffer{limit: 16384}
	cmd.Stdout = output
	cmd.Stderr = diagnostics
	isolateProcess(cmd)
	err := cmd.Run()
	if err != nil || output.exceeded || diagnostics.exceeded || ctx.Err() != nil {
		return nil, ErrQuarantined
	}
	return output.data, nil
}
