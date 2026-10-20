package conversation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"strings"
	"time"
)

var ErrDenied = errors.New("conversation denied")
var ErrConflict = errors.New("inbound key conflict")

// Inbound is a trusted verified-provider port, never an unauthenticated HTTP DTO.
type Inbound struct {
	Tenant, Account, Sender, ProviderMessageID, Text string
	ProviderTimestamp                                time.Time
	ExpectedEmployeeID, ExpectedUnitID               string
}
type Answer struct {
	Text      string   `json:"text"`
	Citations []string `json:"citations"`
	Handoff   bool     `json:"handoff"`
}
type HTTPConfig struct {
	EnabledSyntheticDevelopment bool
	Freshness                   time.Duration
	Clock                       func() time.Time
	Aliases                     func(context.Context, string, string) ([]string, error)
	Advisor                     func(context.Context, api.Actor, string) (Answer, error)
	ResolveCandidates           func(context.Context, cases.Case) (cases.Principal, []cases.Candidate, error)
	// LoadEscalationCase loads through the cases owner using a current authorized
	// worker principal. Timers must continue independently of employee chat access.
	LoadEscalationCase func(context.Context, string, string, string) (cases.Case, error)
	EscalationAfter    time.Duration
}
type Service struct {
	DB     *sql.DB
	Config HTTPConfig
}
type Turn struct {
	Phase          string               `json:"phase"`
	EmployeeID     string               `json:"employee_id"`
	Narrative      string               `json:"narrative"`
	IntakeKey      string               `json:"intake_key"`
	IntakeSnapshot cases.Classification `json:"intake_snapshot"`
	Cases          []string             `json:"cases"`
	Selected       string               `json:"selected,omitempty"`
	Reply          string               `json:"reply"`
	Citations      []string             `json:"citations,omitempty"`
}

func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrDenied
	}
	_, e := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_conversation_turn(tenant text NOT NULL,account text NOT NULL,sender text NOT NULL,payload jsonb NOT NULL,PRIMARY KEY(tenant,account,sender));CREATE TABLE IF NOT EXISTS addon_conversation_inbound(tenant text NOT NULL,account text NOT NULL,message_id text NOT NULL,fingerprint text NOT NULL,response jsonb NOT NULL,PRIMARY KEY(tenant,account,message_id));CREATE TABLE IF NOT EXISTS addon_conversation_timer(tenant text NOT NULL,case_id text PRIMARY KEY,employee_id text NOT NULL,sender text NOT NULL,unit_id text NOT NULL,due_at timestamptz NOT NULL,state text NOT NULL DEFAULT 'pending')`)
	if e != nil {
		return e
	}
	if _, e = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS addon_conversation_activity(tenant text NOT NULL,id text NOT NULL,case_id text NOT NULL DEFAULT '',payload jsonb NOT NULL,PRIMARY KEY(tenant,id))`); e != nil {
		return e
	}
	return delivery.Migrate(ctx, db)
}
func (s Service) employee(ctx context.Context, in Inbound) (directory.Employee, cases.Principal, error) {
	if s.DB == nil || s.Config.Clock == nil || s.Config.Freshness <= 0 || s.Config.Aliases == nil {
		return directory.Employee{}, cases.Principal{}, ErrDenied
	}
	store, e := directory.NewPostgresStore(s.DB, in.Tenant, "verified-channel")
	if e != nil {
		return directory.Employee{}, cases.Principal{}, e
	}
	snap, e := store.Load(ctx)
	if e != nil {
		return directory.Employee{}, cases.Principal{}, e
	}
	emp, e := directory.Match(snap, in.Sender, s.Config.Clock(), s.Config.Freshness)
	if e != nil {
		return emp, cases.Principal{}, ErrDenied
	}
	if in.ExpectedEmployeeID != "" && (emp.ID != in.ExpectedEmployeeID || emp.Zone != in.ExpectedUnitID) {
		return directory.Employee{}, cases.Principal{}, ErrDenied
	}
	aliases, e := s.Config.Aliases(ctx, in.Tenant, emp.ID)
	if e != nil {
		return emp, cases.Principal{}, ErrDenied
	}
	p := cases.Principal{ID: emp.ID, Aliases: aliases, Grants: []cases.Grant{{OrganizationID: in.Tenant, UnitID: emp.Zone, Permission: cases.ReadOwn}, {OrganizationID: in.Tenant, UnitID: emp.Zone, Permission: cases.SubmitOwn}}}
	if emp.Zone == "" {
		return emp, p, ErrDenied
	}
	return emp, p, nil
}
func (s Service) Receive(ctx context.Context, in Inbound) (Turn, error) {
	if in.Tenant == "" || len(in.Tenant) > 256 || in.Account == "" || len(in.Account) > 256 || in.ProviderMessageID == "" || len(in.ProviderMessageID) > 256 || len(in.Text) > 4000 || strings.TrimSpace(in.Text) == "" {
		return Turn{}, ErrDenied
	}
	emp, p, e := s.employee(ctx, in)
	if e != nil {
		return Turn{}, e
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Turn{}, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, digest(in.Tenant+"|provider|"+in.Account+"|"+in.ProviderMessageID)); e != nil {
		return Turn{}, e
	}
	lock := digest(in.Tenant + "\x00" + in.Account + "\x00" + in.Sender)
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lock); e != nil {
		return Turn{}, e
	}
	fingerprint := digest(in.Sender + "\x00" + in.Text)
	var existing, raw []byte
	var fp string
	e = tx.QueryRowContext(ctx, `SELECT fingerprint,response FROM addon_conversation_inbound WHERE tenant=$1 AND account=$2 AND message_id=$3`, in.Tenant, in.Account, in.ProviderMessageID).Scan(&fp, &existing)
	if e == nil {
		if fp != fingerprint {
			return Turn{}, ErrConflict
		}
		var result Turn
		e = json.Unmarshal(existing, &result)
		if e == nil && result.Selected != "" {
			if _, err := (cases.Service{Repository: &cases.SQLRepository{DB: s.DB}}).Read(ctx, p, result.Selected); err != nil {
				return Turn{}, ErrDenied
			}
		}
		if e == nil {
			result = s.currentReferences(ctx, p, result)
		}
		if e == nil {
			e = tx.Commit()
		}
		return result, e
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Turn{}, e
	}
	turn := Turn{EmployeeID: emp.ID, Phase: "ready", Cases: []string{}}
	e = tx.QueryRowContext(ctx, `SELECT payload FROM addon_conversation_turn WHERE tenant=$1 AND account=$2 AND sender=$3`, in.Tenant, in.Account, in.Sender).Scan(&raw)
	if e == nil {
		if e = json.Unmarshal(raw, &turn); e != nil {
			return Turn{}, e
		}
		if turn.EmployeeID != emp.ID {
			turn = Turn{EmployeeID: emp.ID, Phase: "ready", Cases: []string{}}
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return Turn{}, e
	}
	turn.Citations = nil
	message := strings.TrimSpace(in.Text)
	lower := strings.ToLower(message)
	service := cases.Service{Repository: &cases.SQLRepository{DB: s.DB}}
	turn = s.currentReferences(ctx, p, turn)
	caseAck := false
	enquiry := false
	switch {
	case turn.Phase == "narrative":
		if len([]rune(message)) < 12 {
			turn.Reply = "Please describe what happened in at least 12 characters."
		} else {
			turn.Narrative = message
			turn.IntakeSnapshot = cases.Classification{Zone: emp.Zone, Department: emp.Department, CaseType: "employee_request"}
			turn.IntakeKey = "conversation:" + digest(in.Tenant+"\x00"+in.Account+"\x00"+in.Sender+"\x00"+in.ProviderMessageID)
			turn.Phase = "confirm"
			turn.Reply = "Submit this case? Reply CONFIRM or CANCEL."
		}
	case turn.Phase == "confirm":
		if lower == "cancel" {
			turn.Phase = "ready"
			turn.Narrative = ""
			turn.Reply = "Case cancelled."
		} else if lower != "confirm" {
			turn.Reply = "Reply CONFIRM to submit or CANCEL."
		} else {
			if turn.IntakeSnapshot.Zone != emp.Zone {
				return Turn{}, ErrDenied
			}
			r, err := service.Intake(ctx, cases.IntakeRequest{OrganizationID: in.Tenant, UnitID: turn.IntakeSnapshot.Zone, ContactID: emp.ID, Key: turn.IntakeKey, Narrative: turn.Narrative, Snapshot: turn.IntakeSnapshot})
			if err != nil {
				return Turn{}, err
			}
			if r.Outcome != cases.Committed {
				return Turn{}, cases.ErrCommitUnknown
			}
			caseAck = true
			turn.Cases = append(turn.Cases, r.CaseID)
			turn.Selected = r.CaseID
			turn.Phase = "ready"
			turn.Narrative = ""
			turn.Reply = "Case submitted. Reference: " + r.CaseID + ". Reply STATUS for a redacted update."
			delay := s.Config.EscalationAfter
			if delay <= 0 {
				delay = 24 * time.Hour
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO addon_conversation_timer(tenant,case_id,employee_id,sender,unit_id,due_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, in.Tenant, r.CaseID, emp.ID, in.Sender, emp.Zone, s.Config.Clock().Add(delay))
			if e != nil {
				return Turn{}, e
			}
		}
	case lower == "log case" || lower == "logcase" || lower == "new case":
		turn.Phase = "narrative"
		turn.Reply = "Please briefly describe what happened. Do not include unnecessary personal details."
	case lower == "status" || strings.HasPrefix(lower, "status "):
		id := strings.TrimSpace(message[len("status"):])
		if lower == "status" {
			id = ""
		}
		if id == "" && len(turn.Cases) == 1 {
			id = turn.Cases[0]
		}
		if id == "" && len(turn.Cases) > 1 {
			turn.Reply = "Choose a reference with STATUS <reference>: " + strings.Join(turn.Cases, ", ")
		} else {
			allowed := false
			for _, known := range turn.Cases {
				if known == id {
					allowed = true
				}
			}
			if !allowed {
				turn.Reply = "No accessible case selected."
			} else {
				c, err := service.Read(ctx, p, id)
				if err != nil {
					turn.Reply = "No accessible case selected."
				} else {
					turn.Selected = id
					turn.Reply = fmt.Sprintf("Reference: %s. Status: %s.", c.ID, c.State)
				}
			}
		}
	case lower == "handoff" || lower == "human":
		turn.Phase = "ready"
		turn.Reply = "For a human handoff, reply LOG CASE. Your confirmed case will enter the protected routing queue."
	default:
		enquiry = true
		turn.Reply = "Reply LOG CASE to submit a concern, STATUS for your case, or ask an SOP question."
		if s.Config.Advisor != nil {
			a := api.Actor{OrganizationID: in.Tenant, UserID: emp.ID, Aliases: p.Aliases}
			answer, err := s.Config.Advisor(ctx, a, message)
			if err == nil && !answer.Handoff && len(answer.Citations) > 0 && answer.Text != "" {
				turn.Reply = answer.Text
				turn.Citations = answer.Citations
			} else {
				turn.Reply = "I cannot provide a supported answer. Reply LOG CASE for human assistance."
			}
		}
	}
	activityID := digest(in.Tenant + "\x00" + in.Account + "\x00" + in.ProviderMessageID)
	activityCase := ""
	if caseAck || lower == "status" || strings.HasPrefix(lower, "status ") {
		activityCase = turn.Selected
	}
	base := ActivityFact{ID: activityID + ":in", OrganizationID: in.Tenant, UnitID: emp.Zone, Zone: emp.Zone, Department: emp.Department, CaseType: "employee_request", Kind: "inbound", Outcome: "received", OccurredAt: s.Config.Clock()}
	if e = recordActivity(ctx, tx, base, activityCase); e != nil {
		return Turn{}, e
	}
	if enquiry {
		base.ID = activityID + ":enquiry"
		base.Kind = "enquiry"
		if e = recordActivity(ctx, tx, base, activityCase); e != nil {
			return Turn{}, e
		}
	}
	if !caseAck {
		base.ID = activityID + ":out"
		base.Kind = "outbound"
		base.Outcome = "queued"
		if e = recordActivity(ctx, tx, base, activityCase); e != nil {
			return Turn{}, e
		}
	}
	payload, e := json.Marshal(turn)
	if e != nil {
		return Turn{}, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO addon_conversation_turn(tenant,account,sender,payload) VALUES($1,$2,$3,$4) ON CONFLICT(tenant,account,sender) DO UPDATE SET payload=excluded.payload`, in.Tenant, in.Account, in.Sender, payload)
	if e != nil {
		return Turn{}, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO addon_conversation_inbound(tenant,account,message_id,fingerprint,response) VALUES($1,$2,$3,$4,$5)`, in.Tenant, in.Account, in.ProviderMessageID, fingerprint, payload)
	if e != nil {
		return Turn{}, e
	}
	if !caseAck {
		e = delivery.Enqueue(ctx, tx, delivery.Message{ID: digest(in.Tenant + "\x00" + in.Account + "\x00" + in.ProviderMessageID), Tenant: in.Tenant, Account: in.Account, Sender: in.Sender, EmployeeID: emp.ID, CaseID: turn.Selected, Text: turn.Reply})
		if e != nil {
			return Turn{}, e
		}
	}
	if e = tx.Commit(); e != nil {
		return Turn{}, cases.ErrCommitUnknown
	}
	return turn, nil
}

func (s Service) currentReferences(ctx context.Context, p cases.Principal, t Turn) Turn {
	service := cases.Service{Repository: &cases.SQLRepository{DB: s.DB}}
	current := []string{}
	for _, id := range t.Cases {
		if _, e := service.Read(ctx, p, id); e == nil {
			current = append(current, id)
		} else if t.Selected == id {
			t.Selected = ""
		}
	}
	t.Cases = current
	return t
}

// AuthorizeDelivery rechecks current roster identity and current case authorization.
func (s Service) AuthorizeDelivery(ctx context.Context, m delivery.Message) error {
	emp, p, e := s.employee(ctx, Inbound{Tenant: m.Tenant, Sender: m.Sender})
	if e != nil || emp.ID != m.EmployeeID {
		return ErrDenied
	}
	if m.CaseID != "" {
		_, e = (cases.Service{Repository: &cases.SQLRepository{DB: s.DB}}).Read(ctx, p, m.CaseID)
	}
	return e
}
