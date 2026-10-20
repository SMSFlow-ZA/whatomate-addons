package governance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type CommitOutcome string

const (
	Committed  CommitOutcome = "committed"
	RolledBack CommitOutcome = "rolled_back"
	Unknown    CommitOutcome = "unknown"
)

// Repository serializes each scope transaction and persists inventory, policy,
// holds, request approvals, journal, restore keys and audit atomically. These
// interfaces are trusted implementation boundaries, never public host endpoints.
type Repository interface {
	Transact(context.Context, Scope, func(Transaction) error) (CommitOutcome, error)
}
type Transaction interface {
	Get(context.Context, string, string) ([]byte, error)
	List(context.Context, string) ([][]byte, error)
	Put(context.Context, string, string, []byte) error
	AppendEvent(context.Context, Event) error
}
type Service struct{ Repository Repository }

const (
	recordKind  = "inventory"
	ruleKind    = "rule"
	holdKind    = "hold"
	requestKind = "subject_request"
	journalKind = "deletion_journal"
	restoreKind = "restore_key"
)

func get[T any](ctx context.Context, tx Transaction, kind, key string) (T, error) {
	var value T
	raw, e := tx.Get(ctx, kind, key)
	if e != nil {
		return value, e
	}
	e = json.Unmarshal(raw, &value)
	return value, e
}
func list[T any](ctx context.Context, tx Transaction, kind string) ([]T, error) {
	raw, e := tx.List(ctx, kind)
	if e != nil {
		return nil, e
	}
	result := make([]T, 0, len(raw))
	for _, item := range raw {
		var value T
		if e = json.Unmarshal(item, &value); e != nil {
			return nil, e
		}
		result = append(result, value)
	}
	return result, nil
}
func put(ctx context.Context, tx Transaction, kind, key string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	return tx.Put(ctx, kind, key, raw)
}
func id() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(b[:]), nil
}
func (s Service) within(ctx context.Context, p Principal, scope Scope, permission Permission, fn func(Transaction) error) error {
	if !Can(p, scope, permission) {
		return ErrDenied
	}
	if s.Repository == nil {
		return ErrInvalid
	}
	outcome, e := s.Repository.Transact(ctx, scope, fn)
	if outcome != Committed {
		if outcome != RolledBack {
			return errors.Join(ErrCommitUnknown, e)
		}
		if e == nil {
			return errors.New("governance transaction rolled back")
		}
	}
	return e
}
func audit(ctx context.Context, tx Transaction, p Principal, scope Scope, action, reference string) error {
	eventID, e := id()
	if e != nil {
		return e
	}
	return tx.AppendEvent(ctx, Event{ID: eventID, Scope: scope, ActorID: p.ID, Action: action, ReferenceID: reference, RecordedAt: time.Now().UTC()})
}
func (s Service) Register(ctx context.Context, p Principal, r Record) error {
	if e := ValidateRecord(r); e != nil {
		return e
	}
	return s.within(ctx, p, r.Scope, InventoryWrite, func(tx Transaction) error {
		existing, e := get[Record](ctx, tx, recordKind, r.ID)
		if e == nil {
			a, _ := json.Marshal(existing)
			b, _ := json.Marshal(r)
			if string(a) != string(b) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if r.LineageID != r.ID {
			root, e := get[Record](ctx, tx, recordKind, r.LineageID)
			if e != nil {
				return e
			}
			if root.LineageID != root.ID || root.SubjectID != r.SubjectID || root.Scope != r.Scope {
				return ErrInvalid
			}
		}
		if e = put(ctx, tx, recordKind, r.ID, r); e != nil {
			return e
		}
		// Late inventory from restored data inherits outstanding approved deletions.
		requests, e := list[SubjectRequest](ctx, tx, requestKind)
		if e != nil {
			return e
		}
		rules, e := list[Rule](ctx, tx, ruleKind)
		if e != nil {
			return e
		}
		holds, e := list[Hold](ctx, tx, holdKind)
		if e != nil {
			return e
		}
		for _, req := range requests {
			if req.Approved && req.Kind == Delete && req.SubjectID == r.SubjectID {
				entries, e := Plan(req, []Record{r}, rules, holds, time.Now().UTC())
				if e != nil {
					return e
				}
				for _, entry := range entries {
					if e = put(ctx, tx, journalKind, entry.ID, entry); e != nil {
						return e
					}
				}
			}
		}
		return audit(ctx, tx, p, r.Scope, "inventory.register", r.ID)
	})
}
func (s Service) SetRule(ctx context.Context, p Principal, scope Scope, rule Rule) error {
	if !rule.Source.valid() || rule.MinimumRetention < 0 {
		return ErrInvalid
	}
	if rule.Approved {
		rule.ApprovedBy = p.ID
	} else {
		rule.ApprovedBy = ""
	}
	return s.within(ctx, p, scope, PolicyManage, func(tx Transaction) error {
		if e := put(ctx, tx, ruleKind, string(rule.Source), rule); e != nil {
			return e
		}
		return audit(ctx, tx, p, scope, "policy.set", string(rule.Source))
	})
}
func (s Service) SetHold(ctx context.Context, p Principal, h Hold) error {
	if !validReference(h.ID) || !validReference(h.Reference) || h.LineageID == "" {
		return ErrInvalid
	}
	return s.within(ctx, p, h.Scope, HoldManage, func(tx Transaction) error {
		root, e := get[Record](ctx, tx, recordKind, h.LineageID)
		if e != nil {
			return e
		}
		if root.LineageID != root.ID {
			return ErrInvalid
		}
		old, e := get[Hold](ctx, tx, holdKind, h.ID)
		if e == nil && old.LineageID != h.LineageID {
			return ErrConflict
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		if e = put(ctx, tx, holdKind, h.ID, h); e != nil {
			return e
		}
		return audit(ctx, tx, p, h.Scope, "hold.set", h.ID)
	})
}
func (s Service) Request(ctx context.Context, p Principal, req SubjectRequest) (SubjectRequest, error) {
	permission, e := PermissionFor(req.Kind)
	if e != nil {
		return SubjectRequest{}, e
	}
	if !validReference(req.ID) || !validReference(req.SubjectID) || !validReference(req.Reference) {
		return SubjectRequest{}, ErrInvalid
	}
	var result SubjectRequest
	e = s.within(ctx, p, req.Scope, permission, func(tx Transaction) error {
		old, e := get[SubjectRequest](ctx, tx, requestKind, req.ID)
		if e == nil {
			if old.SubjectID != req.SubjectID || old.Kind != req.Kind || old.Reference != req.Reference {
				return ErrConflict
			}
			result = old
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		req.Approved = false
		req.ApprovedBy = ""
		req.CreatedAt = time.Now().UTC()
		req.Version = 1
		if e = put(ctx, tx, requestKind, req.ID, req); e != nil {
			return e
		}
		result = req
		return audit(ctx, tx, p, req.Scope, "request."+string(req.Kind), req.ID)
	})
	if e != nil {
		return SubjectRequest{}, e
	}
	return result, nil
}
func (s Service) ApproveRequest(ctx context.Context, p Principal, scope Scope, requestID string, expected uint64) (SubjectRequest, error) {
	var result SubjectRequest
	e := s.within(ctx, p, scope, DeletionApprove, func(tx Transaction) error {
		req, e := get[SubjectRequest](ctx, tx, requestKind, requestID)
		if e != nil {
			return e
		}
		if req.Kind != Delete {
			return ErrInvalid
		}
		if req.Version != expected {
			return ErrConflict
		}
		req.Approved = true
		req.ApprovedBy = p.ID
		req.Version++
		if e = put(ctx, tx, requestKind, requestID, req); e != nil {
			return e
		}
		result = req
		return audit(ctx, tx, p, scope, "deletion.approve", requestID)
	})
	if e != nil {
		return SubjectRequest{}, e
	}
	return result, nil
}
func (s Service) PlanDeletion(ctx context.Context, p Principal, scope Scope, requestID string) ([]JournalEntry, error) {
	var result []JournalEntry
	e := s.within(ctx, p, scope, DeletionRecord, func(tx Transaction) error {
		req, e := get[SubjectRequest](ctx, tx, requestKind, requestID)
		if e != nil {
			return e
		}
		records, e := list[Record](ctx, tx, recordKind)
		if e != nil {
			return e
		}
		rules, e := list[Rule](ctx, tx, ruleKind)
		if e != nil {
			return e
		}
		holds, e := list[Hold](ctx, tx, holdKind)
		if e != nil {
			return e
		}
		entries, e := Plan(req, records, rules, holds, time.Now().UTC())
		if e != nil {
			return e
		}
		for _, entry := range entries {
			old, e := get[JournalEntry](ctx, tx, journalKind, entry.ID)
			if e == nil {
				if old.Generation == entry.Generation && old.State == RecordedApplied {
					entry = old
				} else {
					entry.Version = old.Version + 1
				}
			} else if !errors.Is(e, ErrNotFound) {
				return e
			}
			if e = put(ctx, tx, journalKind, entry.ID, entry); e != nil {
				return e
			}
			result = append(result, entry)
		}
		return audit(ctx, tx, p, scope, "deletion.plan", requestID)
	})
	if e != nil {
		return nil, e
	}
	return result, nil
}
func (s Service) RecordDeletion(ctx context.Context, p Principal, scope Scope, entryID string, expected uint64, confirmation string) (JournalEntry, error) {
	if !validReference(confirmation) {
		return JournalEntry{}, ErrInvalid
	}
	var result JournalEntry
	e := s.within(ctx, p, scope, DeletionRecord, func(tx Transaction) error {
		entry, e := get[JournalEntry](ctx, tx, journalKind, entryID)
		if e != nil {
			return e
		}
		if entry.Version != expected {
			return ErrConflict
		}
		if entry.State == RecordedApplied {
			return ErrConflict
		}
		r, e := get[Record](ctx, tx, recordKind, entry.RecordID)
		if e != nil {
			return e
		}
		if r.Generation != entry.Generation {
			return ErrConflict
		}
		req, e := get[SubjectRequest](ctx, tx, requestKind, entry.RequestID)
		if e != nil {
			return e
		}
		if !req.Approved || req.Kind != Delete {
			return ErrApproval
		}
		rules, e := list[Rule](ctx, tx, ruleKind)
		if e != nil {
			return e
		}
		holds, e := list[Hold](ctx, tx, holdKind)
		if e != nil {
			return e
		}
		switch stateFor(r, rules, holds, time.Now().UTC()) {
		case BlockedHold:
			return ErrHold
		case BlockedPolicy, BlockedRetention:
			return ErrRetention
		case AwaitingExpiry:
			return ErrExternalPending
		}
		entry.State = RecordedApplied
		entry.ConfirmationReference = confirmation
		entry.Version++
		entry.UpdatedAt = time.Now().UTC()
		if e = put(ctx, tx, journalKind, entry.ID, entry); e != nil {
			return e
		}
		result = entry
		return audit(ctx, tx, p, scope, "deletion.receipt", entry.ID)
	})
	if e != nil {
		return JournalEntry{}, e
	}
	return result, nil
}
func (s Service) Journal(ctx context.Context, p Principal, scope Scope, requestID string) ([]JournalEntry, error) {
	var result []JournalEntry
	e := s.within(ctx, p, scope, JournalRead, func(tx Transaction) error {
		entries, e := list[JournalEntry](ctx, tx, journalKind)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if entry.RequestID == requestID {
				result = append(result, entry)
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return result, nil
}

type restoreKey struct{ RecordID string }

func (s Service) ReconcileRestore(ctx context.Context, p Principal, scope Scope, recordID, key string) ([]JournalEntry, error) {
	if !validReference(key) {
		return nil, ErrInvalid
	}
	var result []JournalEntry
	e := s.within(ctx, p, scope, RestoreReconcile, func(tx Transaction) error {
		prior, e := get[restoreKey](ctx, tx, restoreKind, key)
		if e == nil {
			if prior.RecordID != recordID {
				return ErrConflict
			}
			entries, e := list[JournalEntry](ctx, tx, journalKind)
			if e != nil {
				return e
			}
			for _, entry := range entries {
				if entry.RecordID == recordID {
					result = append(result, entry)
				}
			}
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		r, e := get[Record](ctx, tx, recordKind, recordID)
		if e != nil {
			return e
		}
		r.Generation++
		if e = put(ctx, tx, recordKind, recordID, r); e != nil {
			return e
		}
		requests, e := list[SubjectRequest](ctx, tx, requestKind)
		if e != nil {
			return e
		}
		rules, e := list[Rule](ctx, tx, ruleKind)
		if e != nil {
			return e
		}
		holds, e := list[Hold](ctx, tx, holdKind)
		if e != nil {
			return e
		}
		for _, req := range requests {
			if !req.Approved || req.Kind != Delete || req.SubjectID != r.SubjectID {
				continue
			}
			entries, e := Plan(req, []Record{r}, rules, holds, time.Now().UTC())
			if e != nil {
				return e
			}
			for _, entry := range entries {
				old, e := get[JournalEntry](ctx, tx, journalKind, entry.ID)
				if e == nil {
					entry.Version = old.Version + 1
				} else if !errors.Is(e, ErrNotFound) {
					return e
				}
				if e = put(ctx, tx, journalKind, entry.ID, entry); e != nil {
					return e
				}
				result = append(result, entry)
			}
		}
		if e = put(ctx, tx, restoreKind, key, restoreKey{RecordID: recordID}); e != nil {
			return e
		}
		return audit(ctx, tx, p, scope, "restore.reconcile", recordID)
	})
	if e != nil {
		return nil, e
	}
	return result, nil
}
