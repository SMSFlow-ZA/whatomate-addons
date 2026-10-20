// Package reports computes deterministic reports from an authorized event-time
// snapshot. Durable adapters must reconstruct case facts as of the snapshot,
// including reopenings; today's mutable state is not a historical snapshot.
package reports

import (
	"errors"
	"sort"
	"time"
)

var ErrDenied = errors.New("report access denied or invalid snapshot")

type Actor struct {
	OrganizationID string
	Permissions    []string
}

func (a Actor) has(p string) bool {
	for _, v := range a.Permissions {
		if v == p {
			return true
		}
	}
	return false
}

type Window struct {
	Start, End, AsOf time.Time
	MinGroup         int
}

// Dimension fields are snapshots taken at the case's creation event, not joins
// to the current roster. ResolvedAt is empty when the case is reopened as of AsOf.
type CaseFact struct {
	OrganizationID, ID, Zone, Department, CaseType string
	CreatedAt, ResolvedAt                          time.Time
}
type Activity struct {
	OrganizationID, ID, CaseID string
	At                         time.Time
}
type Facts struct {
	Cases               []CaseFact
	Messages, Enquiries []Activity
}
type Report struct {
	NewCases                int            `json:"new_cases"`
	ResolvedCreatedInPeriod int            `json:"resolved_created_in_period"`
	Backlog                 int            `json:"backlog"`
	Messages                int            `json:"messages"`
	Enquiries               int            `json:"enquiries"`
	ByZone                  map[string]int `json:"by_zone"`
	ByDepartment            map[string]int `json:"by_department"`
	ByCaseType              map[string]int `json:"by_case_type"`
	Partial                 bool           `json:"partial"`
}

type Cell struct {
	Label      string `json:"label"`
	Value      *int   `json:"value"`
	Suppressed bool   `json:"suppressed"`
}

// BoardReport contains no case IDs, narratives, people or drilldown links.
type BoardReport struct {
	NewCases     *int   `json:"new_cases"`
	Backlog      *int   `json:"backlog"`
	Resolved     *int   `json:"resolved_created_in_period"`
	Messages     *int   `json:"messages"`
	Enquiries    *int   `json:"enquiries"`
	ByZone       []Cell `json:"by_zone"`
	ByDepartment []Cell `json:"by_department"`
	ByCaseType   []Cell `json:"by_case_type"`
	Partial      bool   `json:"partial"`
	Suppressed   bool   `json:"suppressed"`
}

func validate(a Actor, f Facts, w Window) error {
	if a.OrganizationID == "" || w.Start.IsZero() || !w.End.After(w.Start) || w.AsOf.Before(w.Start) || w.MinGroup < 2 || w.MinGroup > 100000 || len(f.Cases) > 1000000 || len(f.Messages) > 1000000 || len(f.Enquiries) > 1000000 {
		return ErrDenied
	}
	seen := map[string]bool{}
	for _, c := range f.Cases {
		if c.OrganizationID != a.OrganizationID {
			continue
		}
		if c.ID == "" || seen[c.ID] || c.CreatedAt.IsZero() || (!c.ResolvedAt.IsZero() && c.ResolvedAt.Before(c.CreatedAt)) {
			return ErrDenied
		}
		seen[c.ID] = true
	}
	return nil
}
func during(t time.Time, w Window) bool {
	return !t.Before(w.Start) && t.Before(w.End) && !t.After(w.AsOf)
}
func calculate(a Actor, f Facts, w Window, allow func(CaseFact) bool, board bool) (Report, error) {
	if err := validate(a, f, w); err != nil {
		return Report{}, err
	}
	r := Report{ByZone: map[string]int{}, ByDepartment: map[string]int{}, ByCaseType: map[string]int{}, Partial: w.AsOf.Before(w.End)}
	allowed := map[string]bool{}
	for _, c := range f.Cases {
		if c.OrganizationID != a.OrganizationID || c.CreatedAt.After(w.AsOf) || !allow(c) {
			continue
		}
		allowed[c.ID] = true
		if c.ResolvedAt.IsZero() || c.ResolvedAt.After(w.AsOf) {
			r.Backlog++
		}
		if !during(c.CreatedAt, w) {
			continue
		}
		r.NewCases++
		r.ByZone[c.Zone]++
		r.ByDepartment[c.Department]++
		r.ByCaseType[c.CaseType]++
		if !c.ResolvedAt.IsZero() && !c.ResolvedAt.After(w.AsOf) {
			r.ResolvedCreatedInPeriod++
		}
	}
	count := func(events []Activity) int {
		seen := map[string]bool{}
		n := 0
		for _, e := range events {
			if e.OrganizationID != a.OrganizationID || e.ID == "" || !during(e.At, w) || seen[e.ID] {
				continue
			}
			if !board && !allowed[e.CaseID] {
				continue
			}
			seen[e.ID] = true
			n++
		}
		return n
	}
	r.Messages = count(f.Messages)
	r.Enquiries = count(f.Enquiries)
	return r, nil
}

// Operational always applies the caller's current per-case policy. Activity
// without an authorized CaseID is excluded; pre-case enquiry reports need their
// own protected source adapter and must not be smuggled into an assigned view.
func Operational(a Actor, f Facts, w Window, allow func(CaseFact) bool) (Report, error) {
	if !a.has("reports:operational") || allow == nil {
		return Report{}, ErrDenied
	}
	return calculate(a, f, w, allow, false)
}

func cells(m map[string]int, k int) ([]Cell, bool) {
	labels := make([]string, 0, len(m))
	suppressed := false
	for label, n := range m {
		labels = append(labels, label)
		if n < k {
			suppressed = true
		}
	}
	sort.Strings(labels)
	out := make([]Cell, 0, len(labels))
	for _, label := range labels {
		c := Cell{Label: label, Suppressed: suppressed}
		if !suppressed {
			n := m[label]
			c.Value = &n
		}
		out = append(out, c)
	}
	return out, suppressed
}

// Board suppresses an entire marginal table when any nonzero cell is below k.
// This conservative complementary suppression also omits all headline totals,
// so a suppressed cell cannot be recovered by subtracting visible cells/totals.
// Separate overlapping cohorts/time exports still require disclosure review.
func Board(a Actor, f Facts, w Window) (BoardReport, error) {
	if !a.has("reports:aggregate") {
		return BoardReport{}, ErrDenied
	}
	r, err := calculate(a, f, w, func(CaseFact) bool { return true }, true)
	if err != nil {
		return BoardReport{}, err
	}
	z, zs := cells(r.ByZone, w.MinGroup)
	d, ds := cells(r.ByDepartment, w.MinGroup)
	c, cs := cells(r.ByCaseType, w.MinGroup)
	b := BoardReport{ByZone: z, ByDepartment: d, ByCaseType: c, Partial: r.Partial, Suppressed: zs || ds || cs}
	for _, n := range []int{r.NewCases, r.Backlog, r.ResolvedCreatedInPeriod, r.Messages, r.Enquiries} {
		if n > 0 && n < w.MinGroup {
			b.Suppressed = true
		}
	}
	if b.Suppressed {
		for _, table := range [][]Cell{b.ByZone, b.ByDepartment, b.ByCaseType} {
			for i := range table {
				table[i].Value = nil
				table[i].Suppressed = true
			}
		}
	}
	if !b.Suppressed {
		b.NewCases = &r.NewCases
		b.Backlog = &r.Backlog
		b.Resolved = &r.ResolvedCreatedInPeriod
		b.Messages = &r.Messages
		b.Enquiries = &r.Enquiries
	}
	return b, nil
}
