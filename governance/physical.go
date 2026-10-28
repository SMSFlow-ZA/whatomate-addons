package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// PhysicalAdapter is a trusted storage-owner port. Implementations access only
// their own tables, recheck current domain policy and use the supplied transaction.
// References must be stable opaque IDs; export content is never inventory data.
type PhysicalAdapter interface {
	Name() string
	Discover(context.Context, *sql.Tx, api.Actor, Scope, string) ([]Record, error)
	Export(context.Context, *sql.Tx, api.Actor, Record) (json.RawMessage, error)
	Erase(context.Context, *sql.Tx, api.Actor, Record) error
}
type PhysicalService struct {
	DB             *sql.DB
	Adapters       []PhysicalAdapter
	SubjectAliases func(context.Context, string, string) ([]string, error)
}
type ExportObject struct {
	Record  Record          `json:"record"`
	Content json.RawMessage `json:"content"`
}

func PrincipalForActor(a api.Actor) Principal {
	p := Principal{ID: a.UserID}
	for _, g := range a.Grants {
		if g.UnitID != "" && a.Has(g.Permission) {
			p.Grants = append(p.Grants, Grant{Scope: Scope{a.OrganizationID, g.UnitID}, Permission: Permission(g.Permission)})
		}
	}
	return p
}
func (s PhysicalService) adapter(r Record) PhysicalAdapter {
	for _, a := range s.Adapters {
		if strings.HasPrefix(r.ID, a.Name()+":") {
			return a
		}
	}
	return nil
}
func (s PhysicalService) within(ctx context.Context, a api.Actor, scope Scope, permission Permission, fn func(*sqlTransaction) error) error {
	if _, e := api.ActorFrom(api.WithActor(ctx, a)); e != nil || a.OrganizationID != scope.OrganizationID || !a.Allows(string(permission), scope.UnitID) {
		return ErrDenied
	}
	repo := SQLRepository{DB: s.DB}
	outcome, e := repo.Transact(ctx, scope, func(t Transaction) error { return fn(t.(*sqlTransaction)) })
	if outcome == Unknown {
		return ErrCommitUnknown
	}
	return e
}

// Discover registers actual currently accessible objects. It never claims
// discovery of external worker files, transport copies, logs or backups.
func (s PhysicalService) Discover(ctx context.Context, a api.Actor, scope Scope, subject string) ([]Record, error) {
	var out []Record
	if !validReference(subject) {
		return nil, ErrInvalid
	}
	var err error
	ctx, err = s.subjectContext(ctx, a.OrganizationID, subject)
	if err != nil {
		return nil, err
	}
	e := s.within(ctx, a, scope, InventoryWrite, func(t *sqlTransaction) error {
		for _, adapter := range s.Adapters {
			records, e := adapter.Discover(ctx, t.tx, a, scope, subject)
			if e != nil {
				return e
			}
			for _, r := range records {
				if ValidateRecord(r) != nil || r.Scope != scope || r.SubjectID != subject || !strings.HasPrefix(r.ID, adapter.Name()+":") {
					return ErrInvalid
				}
				old, e := get[Record](ctx, t, recordKind, r.ID)
				if e == nil {
					if old.Scope != r.Scope || old.SubjectID != r.SubjectID || old.Reference != r.Reference || !sameHoldLineages(old, r) {
						return ErrConflict
					}
					r = old
				} else if e != ErrNotFound {
					return e
				} else {
					if e = put(ctx, t, recordKind, r.ID, r); e != nil {
						return e
					}
				}
				out = append(out, r)
			}
		}
		for _, r := range out {
			if r.LineageID != r.ID {
				root, e := get[Record](ctx, t, recordKind, r.LineageID)
				if e != nil {
					return e
				}
				if root.SubjectID != r.SubjectID || root.Scope != r.Scope || root.LineageID != root.ID {
					return ErrInvalid
				}
			}
		}
		return audit(ctx, t, PrincipalForActor(a), scope, "inventory.discover", subject)
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}
func (s PhysicalService) ExportSubject(ctx context.Context, a api.Actor, scope Scope, subject string) ([]ExportObject, error) {
	var out []ExportObject
	var err error
	ctx, err = s.subjectContext(ctx, a.OrganizationID, subject)
	if err != nil {
		return nil, err
	}
	e := s.within(ctx, a, scope, ExportRequest, func(t *sqlTransaction) error {
		records, e := list[Record](ctx, t, recordKind)
		if e != nil {
			return e
		}
		for _, r := range records {
			if r.SubjectID != subject {
				continue
			}
			adapter := s.adapter(r)
			if adapter == nil {
				continue
			}
			content, e := adapter.Export(ctx, t.tx, a, r)
			if e != nil {
				return e
			}
			out = append(out, ExportObject{r, content})
		}
		return audit(ctx, t, PrincipalForActor(a), scope, "subject.export", subject)
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

// EraseRequest rechecks approval, current scope, retention and every lineage
// hold under the SAME SQL transaction as all owner erasures and journal updates.
// It does not erase the minimal governance journal or external pending copies.
func (s PhysicalService) EraseRequest(ctx context.Context, a api.Actor, scope Scope, requestID string) ([]JournalEntry, error) {
	var out []JournalEntry
	e := s.within(ctx, a, scope, DeletionRecord, func(t *sqlTransaction) error {
		req, e := get[SubjectRequest](ctx, t, requestKind, requestID)
		if e != nil {
			return e
		}
		if !req.Approved || req.Kind != Delete {
			return ErrApproval
		}
		var err error
		ctx, err = s.subjectContext(ctx, a.OrganizationID, req.SubjectID)
		if err != nil {
			return err
		}
		// Rediscover current owner objects in this transaction. A preview made
		// earlier is never an exhaustive deletion authorization or object list.
		for _, adapter := range s.Adapters {
			fresh, e := adapter.Discover(ctx, t.tx, a, scope, req.SubjectID)
			if e != nil {
				return e
			}
			for _, r := range fresh {
				if ValidateRecord(r) != nil || r.Scope != scope || r.SubjectID != req.SubjectID || !strings.HasPrefix(r.ID, adapter.Name()+":") {
					return ErrInvalid
				}
				old, e := get[Record](ctx, t, recordKind, r.ID)
				if e == ErrNotFound {
					if e = put(ctx, t, recordKind, r.ID, r); e != nil {
						return e
					}
				} else if e != nil {
					return e
				} else if old.SubjectID != r.SubjectID || old.Scope != r.Scope || old.Reference != r.Reference || !sameHoldLineages(old, r) {
					return ErrConflict
				}
			}
		}
		records, e := list[Record](ctx, t, recordKind)
		if e != nil {
			return e
		}
		rules, e := list[Rule](ctx, t, ruleKind)
		if e != nil {
			return e
		}
		holds, e := list[Hold](ctx, t, holdKind)
		if e != nil {
			return e
		}
		// Check all objects before any physical mutation, including already applied
		// objects: a restored copy must never escape a newly imposed hold.
		entries, e := Plan(req, records, rules, holds, time.Now().UTC())
		if e != nil {
			return e
		}
		// Derivative policies still consult their current parent case; erase parent
		// cases last, independently of discovery/registration order.
		sort.SliceStable(entries, func(i, j int) bool {
			return !strings.HasPrefix(entries[i].RecordID, "cases:") && strings.HasPrefix(entries[j].RecordID, "cases:")
		})
		for _, entry := range entries {
			switch entry.State {
			case BlockedHold:
				return ErrHold
			case BlockedPolicy, BlockedRetention:
				return ErrRetention
			}
		}
		for _, entry := range entries {
			var r Record
			for _, v := range records {
				if v.ID == entry.RecordID {
					r = v
					break
				}
			}
			old, oldErr := get[JournalEntry](ctx, t, journalKind, entry.ID)
			if oldErr != nil && oldErr != ErrNotFound {
				return oldErr
			}
			if oldErr == nil {
				entry.Version = old.Version + 1
			}
			if oldErr == nil && old.Generation == entry.Generation && old.State == RecordedApplied {
				out = append(out, old)
				continue
			}
			adapter := s.adapter(r)
			if entry.State == Pending && adapter != nil {
				if e = adapter.Erase(ctx, t.tx, a, r); e != nil {
					return e
				}
				entry.State = RecordedApplied
				entry.ConfirmationReference = "sql-transaction:" + entry.ID
			} else if entry.State == Pending {
				entry.State = AwaitingExpiry
			}
			if e = put(ctx, t, journalKind, entry.ID, entry); e != nil {
				return e
			}
			out = append(out, entry)
		}
		return audit(ctx, t, PrincipalForActor(a), scope, "deletion.physical", requestID)
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}

func (s PhysicalService) Inventory(ctx context.Context, a api.Actor, scope Scope) ([]Record, error) {
	var out []Record
	e := s.within(ctx, a, scope, JournalRead, func(t *sqlTransaction) error { var e error; out, e = list[Record](ctx, t, recordKind); return e })
	if e != nil {
		return nil, e
	}
	return out, nil
}

// VisibleInventory applies current source-owner read policy before metadata is
// returned to an operator. Inventory does not confer access to a source object.
func (s PhysicalService) VisibleInventory(ctx context.Context, a api.Actor, scope Scope) ([]Record, error) {
	var out []Record
	e := s.within(ctx, a, scope, JournalRead, func(t *sqlTransaction) error {
		records, e := list[Record](ctx, t, recordKind)
		if e != nil {
			return e
		}
		for _, r := range records {
			adapter := s.adapter(r)
			if adapter == nil {
				continue
			}
			if _, e = adapter.Export(ctx, t.tx, a, r); e != nil {
				if e == ErrDenied || e == ErrNotFound {
					continue
				}
				return e
			}
			out = append(out, r)
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}
