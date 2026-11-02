package governance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync/atomic"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

// DelegationLease is an opaque durable attempt identity, never authentication.
type DelegationLease struct {
	Tenant, ID, Token string
	Version           uint64
}

// DelegationGrant is constructed by a trusted host validator from locked durable
// approvals and current source policy, never decoded from an HTTP worker actor.
const ScheduledErasurePurpose = "scheduled_subject_erasure"

type DelegationGrant struct {
	Purpose                       string
	Lease                         DelegationLease
	Subject                       string
	Aliases                       []string
	Requests                      []ScopedDeletion
	PlanDigest, RestoreGeneration string
	ExpiresAt                     time.Time
	Requester, Reviewer           api.Actor
	Records                       []Record
}
type DelegationValidator interface {
	ValidateDelegationTx(context.Context, *sql.Tx, DelegationLease) (DelegationGrant, error)
	RecheckDelegationTx(context.Context, *sql.Tx, DelegationLease, string, string) error
	ValidateDelegatedPlanTx(context.Context, *sql.Tx, DelegationGrant, []Record) error
}

// DelegatedExecutor is configured at trusted composition with a host validator.
// There is no exported constructor for an active DelegatedAuthority.
type DelegatedExecutor struct{ Validate DelegationValidator }

// DelegatedAuthority is neither serializable nor constructible by a client.
// Its exact transaction, lifetime, current locked lease and actor snapshots are
// checked at every source call. It never changes MFA or session authentication.
type DelegatedAuthority struct {
	tx           *sql.Tx
	grant        DelegationGrant
	alive        *atomic.Bool
	validator    DelegationValidator
	actorHashes  map[string]bool
	scopes       map[Scope]bool
	fingerprints map[string]string
}

func delegateHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func RecordFingerprint(r Record) string { return delegateHash(r) }
func actorCopy(a api.Actor) api.Actor {
	a.Permissions = append([]string(nil), a.Permissions...)
	a.Grants = append([]api.Grant(nil), a.Grants...)
	a.Aliases = append([]string(nil), a.Aliases...)
	return a
}
func (s DelegatedExecutor) WithValidatedDelegationTx(ctx context.Context, tx *sql.Tx, lease DelegationLease, fn func(*DelegatedAuthority) error) error {
	if tx == nil || s.Validate == nil || fn == nil || lease.Tenant == "" || lease.ID == "" || lease.Token == "" || lease.Version == 0 {
		return ErrDenied
	}
	g, e := s.Validate.ValidateDelegationTx(ctx, tx, lease)
	if e != nil {
		return e
	}
	if g.Purpose != ScheduledErasurePurpose || g.Lease != lease || g.Subject == "" || g.PlanDigest == "" || g.RestoreGeneration == "" || !time.Now().Before(g.ExpiresAt) || len(g.Requests) == 0 || len(g.Requests) > 32 || len(g.Records) == 0 {
		return ErrDenied
	}
	if g.Requester.UserID == "" || g.Reviewer.UserID == "" || g.Requester.UserID == g.Reviewer.UserID {
		return ErrDenied
	}
	cap := &DelegatedAuthority{alive: &atomic.Bool{}, tx: tx, validator: s.Validate, grant: g, actorHashes: map[string]bool{}, scopes: map[Scope]bool{}, fingerprints: map[string]string{}}
	cap.grant.Requester = actorCopy(g.Requester)
	cap.grant.Reviewer = actorCopy(g.Reviewer)
	cap.grant.Aliases = append([]string(nil), g.Aliases...)
	cap.grant.Requests = append([]ScopedDeletion(nil), g.Requests...)
	cap.grant.Records = append([]Record(nil), g.Records...)
	for i := range cap.grant.Records {
		cap.grant.Records[i].HoldLineageIDs = append([]string(nil), g.Records[i].HoldLineageIDs...)
	}
	for _, a := range []api.Actor{g.Requester, g.Reviewer} {
		if a.OrganizationID != lease.Tenant || a.MFA || a.PrimarySessionAuthorized || a.SessionID != "" {
			return ErrDenied
		}
		cap.actorHashes[delegateHash(a)] = true
	}
	for _, r := range g.Requests {
		if r.Scope.OrganizationID != lease.Tenant || !r.Scope.valid() || r.RequestID == "" || cap.scopes[r.Scope] {
			return ErrInvalid
		}
		cap.scopes[r.Scope] = true
	}
	for _, r := range g.Records {
		if !cap.scopes[r.Scope] || r.SubjectID != g.Subject || ValidateRecord(r) != nil {
			return ErrInvalid
		}
		key := delegateHash([]any{r.Scope, r.ID})
		if _, exists := cap.fingerprints[key]; exists {
			return ErrInvalid
		}
		cap.fingerprints[key] = RecordFingerprint(r)
	}
	cap.alive.Store(true)
	defer cap.alive.Store(false)
	return fn(cap)
}
func (c *DelegatedAuthority) check(ctx context.Context, tx *sql.Tx, a api.Actor) error {
	if c == nil || c.alive == nil || !c.alive.Load() || tx == nil || tx != c.tx || a.MFA || a.PrimarySessionAuthorized || a.SessionID != "" || !c.actorHashes[delegateHash(a)] || !time.Now().Before(c.grant.ExpiresAt) {
		return ErrDenied
	}
	// A committed/rolled-back sql.Tx must invalidate the permit even if an errant
	// callback retains the pointer before its deferred lifetime invalidation.
	var open int
	if e := tx.QueryRowContext(ctx, `SELECT 1`).Scan(&open); e != nil {
		return ErrDenied
	}
	// The durable lease is locked by the host validator for the entire callback.
	// Rechecking exact lease identity/version and purpose protects same-tx changes.
	if e := c.validator.RecheckDelegationTx(ctx, tx, c.grant.Lease, c.grant.PlanDigest, c.grant.RestoreGeneration); e != nil {
		return ErrDenied
	}

	return nil
}
func (c *DelegatedAuthority) CheckDiscover(ctx context.Context, tx *sql.Tx, a api.Actor, scope Scope, subject string) error {
	if e := c.check(ctx, tx, a); e != nil {
		return e
	}
	if !c.scopes[scope] || subject != c.grant.Subject {
		return ErrDenied
	}
	return nil
}
func (c *DelegatedAuthority) CheckErase(ctx context.Context, tx *sql.Tx, a api.Actor, r Record) error {
	if e := c.CheckDiscover(ctx, tx, a, r.Scope, r.SubjectID); e != nil {
		return e
	}
	if c.fingerprints[delegateHash([]any{r.Scope, r.ID})] != RecordFingerprint(r) {
		return ErrConflict
	}
	return nil
}
func (c *DelegatedAuthority) Actors() []api.Actor {
	if c == nil || c.alive == nil || !c.alive.Load() {
		return nil
	}
	return []api.Actor{actorCopy(c.grant.Requester), actorCopy(c.grant.Reviewer)}
}
func (c *DelegatedAuthority) Subject() string {
	if c == nil || c.alive == nil || !c.alive.Load() {
		return ""
	}
	return c.grant.Subject
}
func (c *DelegatedAuthority) Requests() []ScopedDeletion {
	if c == nil || c.alive == nil || !c.alive.Load() {
		return nil
	}
	return append([]ScopedDeletion(nil), c.grant.Requests...)
}
func (c *DelegatedAuthority) Records() []Record {
	if c == nil || c.alive == nil || !c.alive.Load() {
		return nil
	}
	out := append([]Record(nil), c.grant.Records...)
	for i := range out {
		out[i].HoldLineageIDs = append([]string(nil), out[i].HoldLineageIDs...)
	}
	return out
}

// DelegatedPhysicalAdapter is explicit, source-owned and intentionally separate
// from interactive interfaces. Missing support is a hard denial, not fallback.
type DelegatedPhysicalAdapter interface {
	PhysicalAdapter
	DiscoverDelegatedTx(context.Context, *sql.Tx, api.Actor, Scope, string, *DelegatedAuthority) ([]Record, error)
	EraseDelegatedTx(context.Context, *sql.Tx, api.Actor, Record, *DelegatedAuthority) error
}

func SameDelegatedInventory(expected, actual []Record) bool {
	keys := func(rows []Record) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = RecordFingerprint(r)
		}
		sort.Strings(out)
		return out
	}
	return delegateHash(keys(expected)) == delegateHash(keys(actual))
}

func (c *DelegatedAuthority) CheckCurrentPlan(ctx context.Context, tx *sql.Tx, records []Record) error {
	if c == nil {
		return ErrDenied
	}
	if e := c.check(ctx, tx, c.grant.Requester); e != nil {
		return e
	}
	if !SameDelegatedInventory(c.grant.Records, records) {
		return ErrConflict
	}
	return c.validator.ValidateDelegatedPlanTx(ctx, tx, c.grant, records)
}

// DelegatedWorkerController must perform source-specific actual cleanup and
// reconcile its durable original-identity receipt before any repeated deletion.
// It receives the same locked transaction and the typed exact-record permit.
type DelegatedWorkerController func(context.Context, *sql.Tx, api.Actor, Record, *DelegatedAuthority) (WorkerErasureReceipt, error)
