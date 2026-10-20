package directory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// LegacyWorker is an optional trusted local process adapter. The executable and
// prefix arguments come from host deployment configuration, never an upload.
// Run the supplied Linux worker with an unprivileged, network-denied container
// and OS memory/process limits; this adapter is not an OS sandbox by itself.
type LegacyWorker struct {
	Executable string
	Arguments  []string
	Timeout    time.Duration
}

var ErrLegacyWorker = errors.New("legacy XLS worker rejected import")

func (w LegacyWorker) Read(ctx context.Context, data []byte, sheet string, l Limits) ([][]string, error) {
	magic := []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}
	if !filepath.IsAbs(w.Executable) || !l.valid() || len(data) == 0 || int64(len(data)) > l.MaxFileBytes || !bytes.HasPrefix(data, magic) || sheet == "" || len(sheet) > 128 || w.Timeout <= 0 || w.Timeout > 15*time.Second {
		return nil, ErrLegacyWorker
	}
	ctx, cancel := context.WithTimeout(ctx, w.Timeout)
	defer cancel()
	args := append([]string(nil), w.Arguments...)
	args = append(args, "--sheet", sheet, "--max-file", strconv.FormatInt(l.MaxFileBytes, 10), "--max-rows", strconv.Itoa(l.MaxRows), "--max-columns", strconv.Itoa(l.MaxColumns), "--max-cell", strconv.Itoa(l.MaxCellBytes))
	cmd := exec.CommandContext(ctx, w.Executable, args...)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stderr = io.Discard
	cmd.Env = []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH=/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE=1"}
	out := &boundedOutput{limit: 64 << 20, cancel: cancel}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, ErrLegacyWorker
	}
	return decodeLegacy(out.buf.Bytes(), l)
}

type boundedOutput struct {
	buf    bytes.Buffer
	limit  int
	cancel context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buf.Len() {
		b.cancel()
		return 0, ErrLegacyWorker
	}
	return b.buf.Write(p)
}
func decodeLegacy(raw []byte, l Limits) ([][]string, error) {
	if !l.valid() || len(raw) > 64<<20 {
		return nil, ErrLegacyWorker
	}
	var result struct {
		Rows   [][]string `json:"rows"`
		Parser string     `json:"parser"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || result.Parser != "xlrd-2.0.2" || len(result.Rows) > l.MaxRows {
		return nil, ErrLegacyWorker
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, ErrLegacyWorker
	}
	total := int64(0)
	for _, row := range result.Rows {
		if len(row) > l.MaxColumns {
			return nil, ErrLegacyWorker
		}
		for _, v := range row {
			if len(v) > l.MaxCellBytes {
				return nil, ErrLegacyWorker
			}
			total += int64(len(v))
			if total > l.MaxExpandedBytes {
				return nil, ErrLegacyWorker
			}
		}
	}
	return result.Rows, nil
}
