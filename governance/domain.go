// Package governance maintains reference-only record policies and a deletion /
// restore journal. It performs no physical deletion or provider messaging.
package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
)

type Scope struct{ OrganizationID, UnitID string }

func (s Scope) valid() bool { return s.OrganizationID != "" && s.UnitID != "" }

type Source string

const (
	Core      Source = "core"
	Case      Source = "case"
	Knowledge Source = "knowledge"
	Vector    Source = "vector"
	Audit     Source = "audit"
	Backup    Source = "backup"
	Provider  Source = "provider"
)

func (s Source) valid() bool {
	switch s {
	case Core, Case, Knowledge, Vector, Audit, Backup, Provider:
		return true
	}
	return false
}

type Permission string

const (
	InventoryWrite    Permission = "governance:inventory.write"
	HoldManage        Permission = "governance:hold.manage"
	PolicyManage      Permission = "governance:policy.manage"
	AccessRequest     Permission = "governance:request.access"
	CorrectionRequest Permission = "governance:request.correct"
	ExportRequest     Permission = "governance:request.export"
	DeleteRequest     Permission = "governance:request.delete"
	DeletionApprove   Permission = "governance:deletion.approve"
	DeletionRecord    Permission = "governance:deletion.record"
	RestoreReconcile  Permission = "governance:restore.reconcile"
	JournalRead       Permission = "governance:journal.read"
)

type Grant struct {
	Scope      Scope
	Permission Permission
}
type Principal struct {
	ID     string
	Grants []Grant
}

func Can(p Principal, s Scope, permission Permission) bool {
	if p.ID == "" || !s.valid() {
		return false
	}
	for _, g := range p.Grants {
		if g.Scope == s && g.Permission == permission {
			return true
		}
	}
	return false
}

type RequestKind string

const (
	Access  RequestKind = "access"
	Correct RequestKind = "correct"
	Export  RequestKind = "export"
	Delete  RequestKind = "delete"
)

func PermissionFor(kind RequestKind) (Permission, error) {
	switch kind {
	case Access:
		return AccessRequest, nil
	case Correct:
		return CorrectionRequest, nil
	case Export:
		return ExportRequest, nil
	case Delete:
		return DeleteRequest, nil
	}
	return "", ErrInvalid
}

type Record struct {
	ID                     string
	Scope                  Scope
	SubjectID, LineageID   string
	Source                 Source
	Reference              string
	CreatedAt, RetainUntil time.Time
	Generation             uint64
}
type Rule struct {
	Source           Source
	MinimumRetention time.Duration
	Approved         bool
	ApprovedBy       string
}
type Hold struct {
	ID                   string
	Scope                Scope
	LineageID, Reference string
	Active               bool
}
type SubjectRequest struct {
	ID         string
	Scope      Scope
	SubjectID  string
	Kind       RequestKind
	Reference  string
	Approved   bool
	ApprovedBy string
	CreatedAt  time.Time
	Version    uint64
}
type JournalState string

const (
	Pending          JournalState = "pending"
	RecordedApplied  JournalState = "recorded_applied"
	AwaitingExpiry   JournalState = "awaiting_external_expiry"
	BlockedHold      JournalState = "blocked_hold"
	BlockedRetention JournalState = "blocked_retention"
	BlockedPolicy    JournalState = "blocked_policy"
)

type JournalEntry struct {
	ID, RequestID, RecordID string
	Scope                   Scope
	Source                  Source
	State                   JournalState
	Generation, Version     uint64
	ConfirmationReference   string
	UpdatedAt               time.Time
}
type Event struct {
	ID                           string
	Scope                        Scope
	ActorID, Action, ReferenceID string
	RecordedAt                   time.Time
}

var (
	ErrInvalid         = errors.New("invalid governance input")
	ErrDenied          = errors.New("governance access denied")
	ErrApproval        = errors.New("approved deletion request required")
	ErrHold            = errors.New("legal hold blocks deletion")
	ErrRetention       = errors.New("retention policy blocks deletion")
	ErrExternalPending = errors.New("external provider or backup expiry remains pending")
	ErrConflict        = errors.New("governance version or idempotency conflict")
	ErrNotFound        = errors.New("governance record not found")
	ErrCommitUnknown   = errors.New("governance commit confirmation unknown; reconcile durable state")
)

// References are opaque identifiers, never case narratives or support transcripts.
// This syntactic boundary cannot determine semantic sensitivity; hosts must only
// register references to separately authorized storage.
func validReference(ref string) bool {
	if len(ref) == 0 || len(ref) > 256 {
		return false
	}
	return !strings.ContainsFunc(ref, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
func ValidateRecord(r Record) error {
	if !r.Scope.valid() || r.ID == "" || r.SubjectID == "" || r.LineageID == "" || !r.Source.valid() || !validReference(r.Reference) || r.CreatedAt.IsZero() || r.Generation == 0 {
		return ErrInvalid
	}
	return nil
}
func Plan(req SubjectRequest, records []Record, rules []Rule, holds []Hold, now time.Time) ([]JournalEntry, error) {
	if req.Kind != Delete || !req.Approved {
		return nil, ErrApproval
	}
	if !req.Scope.valid() || req.ID == "" || req.SubjectID == "" || now.IsZero() {
		return nil, ErrInvalid
	}
	var result []JournalEntry
	for _, r := range records {
		if r.Scope != req.Scope || r.SubjectID != req.SubjectID {
			continue
		}
		if e := ValidateRecord(r); e != nil {
			return nil, e
		}
		state := stateFor(r, rules, holds, now)
		result = append(result, JournalEntry{ID: journalID(req.ID, r.ID), RequestID: req.ID, RecordID: r.ID, Scope: r.Scope, Source: r.Source, State: state, Generation: r.Generation, Version: 1, UpdatedAt: now.UTC()})
	}
	return result, nil
}
func stateFor(r Record, rules []Rule, holds []Hold, now time.Time) JournalState {
	for _, h := range holds {
		if h.Active && h.Scope == r.Scope && h.LineageID == r.LineageID {
			return BlockedHold
		}
	}
	var rule *Rule
	for i := range rules {
		if rules[i].Source == r.Source {
			rule = &rules[i]
			break
		}
	}
	if rule == nil || !rule.Approved || rule.MinimumRetention < 0 {
		return BlockedPolicy
	}
	if now.Before(r.RetainUntil) || now.Before(r.CreatedAt.Add(rule.MinimumRetention)) {
		return BlockedRetention
	}
	if r.Source == Backup || r.Source == Provider {
		return AwaitingExpiry
	}
	return Pending
}

func journalID(requestID, recordID string) string {
	encoded, _ := json.Marshal([2]string{requestID, recordID})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
