package cases

import (
	"context"
	"encoding/json"
)

// MemoryRepository is a serialized, non-durable reference adapter for tests and
// development only. It is never a production persistence or outbox dispatcher.
type MemoryRepository struct {
	gate    chan struct{}
	cases   map[string]Case
	intakes map[string]intakeRecord
	outbox  map[string]OutboxMessage
}
type intakeRecord struct{ Fingerprint, CaseID string }

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{gate: make(chan struct{}, 1), cases: map[string]Case{}, intakes: map[string]intakeRecord{}, outbox: map[string]OutboxMessage{}}
}
func (r *MemoryRepository) Begin(ctx context.Context) (Transaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.lock(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		r.unlock()
		return nil, err
	}
	t := &memoryTx{owner: r, cases: map[string]Case{}, intakes: map[string]intakeRecord{}, outbox: map[string]OutboxMessage{}}
	for k, v := range r.cases {
		t.cases[k] = clone(v)
	}
	for k, v := range r.intakes {
		t.intakes[k] = v
	}
	for k, v := range r.outbox {
		t.outbox[k] = v
	}
	return t, nil
}

type memoryTx struct {
	owner   *MemoryRepository
	cases   map[string]Case
	intakes map[string]intakeRecord
	outbox  map[string]OutboxMessage
	done    bool
}

func (t *memoryTx) Intake(ctx context.Context, org, key, fp string, c Case, m OutboxMessage) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	if t.done {
		return "", ErrInvalid
	}
	encoded, _ := json.Marshal([2]string{org, key})
	k := string(encoded)
	if old, ok := t.intakes[k]; ok {
		if old.Fingerprint != fp {
			return "", ErrIdempotencyConflict
		}
		return old.CaseID, nil
	}
	if _, ok := t.cases[c.ID]; ok {
		return "", ErrConflict
	}
	t.cases[c.ID] = clone(c)
	t.intakes[k] = intakeRecord{fp, c.ID}
	t.outbox[m.ID] = m
	return c.ID, nil
}
func (t *memoryTx) Load(ctx context.Context, id string) (Case, error) {
	if e := ctx.Err(); e != nil {
		return Case{}, e
	}
	if t.done {
		return Case{}, ErrInvalid
	}
	c, ok := t.cases[id]
	if !ok {
		return Case{}, ErrNotFound
	}
	return clone(c), nil
}
func (t *memoryTx) Save(ctx context.Context, c Case, expected uint64) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if t.done {
		return ErrInvalid
	}
	old, ok := t.cases[c.ID]
	if !ok {
		return ErrNotFound
	}
	if old.Version != expected || c.Version != expected+1 {
		return ErrConflict
	}
	if c.OrganizationID != old.OrganizationID || c.UnitID != old.UnitID || c.ContactID != old.ContactID {
		return ErrInvalid
	}
	t.cases[c.ID] = clone(c)
	return nil
}
func (t *memoryTx) Commit(ctx context.Context) (CommitOutcome, error) {
	if t.done {
		return RolledBack, ErrInvalid
	}
	if e := ctx.Err(); e != nil {
		t.Rollback(ctx)
		return RolledBack, e
	}
	t.owner.cases = t.cases
	t.owner.intakes = t.intakes
	t.owner.outbox = t.outbox
	t.done = true
	t.owner.unlock()
	return Committed, nil
}
func (t *memoryTx) Rollback(context.Context) error {
	if !t.done {
		t.done = true
		if t.owner != nil {
			t.owner.unlock()
		}
	}
	return nil
}

// Snapshot returns detached data for test/development inspection, without access
// filtering. Hosts must never expose this diagnostic method to end users.
func (r *MemoryRepository) Snapshot() ([]Case, []OutboxMessage) {
	r.lock(context.Background())
	defer r.unlock()
	var c []Case
	var o []OutboxMessage
	for _, v := range r.cases {
		c = append(c, clone(v))
	}
	for _, v := range r.outbox {
		o = append(o, v)
	}
	return c, o
}

// Seed is development-only setup; it bypasses domain validation and access checks.
func (r *MemoryRepository) Seed(c Case) {
	r.lock(context.Background())
	defer r.unlock()
	r.cases[c.ID] = clone(c)
}

func (r *MemoryRepository) lock(ctx context.Context) error {
	select {
	case r.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *MemoryRepository) unlock() { <-r.gate }
