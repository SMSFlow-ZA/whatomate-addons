package knowledge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
)

func TestHTTPPublicationConflictsPreserveApprovedJob(t *testing.T) {
	db, tenant := knowledgeHTTPFixture(t)
	ctx := context.Background()
	if err := MigrateHTTP(ctx, db); err != nil {
		t.Fatal(err)
	}
	actors := map[string]api.Actor{}
	for user, action := range map[string]string{"u": "upload", "r": "review", "p": "publish", "x": "retire"} {
		permission := "knowledge:" + action
		actors[user] = api.Actor{OrganizationID: tenant, UserID: user, SessionID: "session", MFA: true, Permissions: []string{permission}, Grants: []api.Grant{{Permission: permission, Audience: "staff"}}}
	}
	h := NewHTTPHandler(db, HTTPConfig{Enabled: true, Authorize: func(_ context.Context, org, user, permission, audience string) bool {
		return org == tenant && actors[user].AudienceAllowed(permission, audience)
	}})
	q := NewProcessingQueue(db)
	prepare := func(version string, effective time.Time) string {
		t.Helper()
		w := uploadRequest(h, actors["u"], UploadMetadata{DocumentID: "synthetic-policy", Version: version, Filename: "policy.pdf", MediaType: "application/pdf", Audiences: []string{"staff"}, Effective: effective})
		if w.Code != 202 {
			t.Fatalf("upload: %d %s", w.Code, w.Body)
		}
		var response struct{ Data struct{ ID string } }
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		job, err := q.Claim(ctx, "conflict-test-worker", time.Minute)
		if err != nil || job.ID != response.Data.ID {
			t.Fatalf("claim: %v, got %s want %s", err, job.ID, response.Data.ID)
		}
		if err := q.Complete(ctx, job, []Page{{Number: 1, Text: "Synthetic policy revision " + version, Method: "text", ExtractorRevision: "test-1"}}, Evidence{ContentHash: job.InputHash, ScannerRevision: "test-1", MalwareClean: true, ExtractionComplete: true}); err != nil {
			t.Fatal(err)
		}
		if w = knowledgeRequest(h, actors["r"], "POST", "jobs/"+job.ID+"/approve", `{"expected_version":3}`); w.Code != 200 {
			t.Fatalf("approve: %d %s", w.Code, w.Body)
		}
		return job.ID
	}
	effective := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	first := prepare("1", effective)
	if w := knowledgeRequest(h, actors["p"], "POST", "jobs/"+first+"/publish", `{"expected_version":4}`); w.Code != 200 {
		t.Fatalf("publish first: %d %s", w.Code, w.Body)
	}
	if w := knowledgeRequest(h, actors["x"], "POST", "jobs/"+first+"/retire", `{"expected_version":5}`); w.Code != 200 {
		t.Fatalf("retire: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct {
		name, version string
		effective     time.Time
	}{{"retired effective timestamp", "2", effective}, {"existing version", "1", effective.Add(time.Minute)}} {
		t.Run(tc.name, func(t *testing.T) {
			id := prepare(tc.version, tc.effective)
			w := knowledgeRequest(h, actors["p"], "POST", "jobs/"+id+"/publish", `{"expected_version":4}`)
			if w.Code != 409 || !strings.Contains(w.Body.String(), `"publication_conflict"`) {
				t.Errorf("expected known publication conflict: %d %s", w.Code, w.Body)
			}
			var status string
			var version int
			if err := db.QueryRowContext(ctx, `SELECT status,version FROM addon_knowledge_jobs WHERE tenant=$1 AND id=$2`, tenant, id).Scan(&status, &version); err != nil || status != "approved" || version != 4 {
				t.Fatalf("failed publication changed job: %s/%d, %v", status, version, err)
			}
			var count int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM addon_knowledge_publications WHERE tenant=$1`, tenant).Scan(&count); err != nil || count != 1 {
				t.Fatalf("failed publication persisted: count=%d err=%v", count, err)
			}
		})
	}
	valid := prepare("3", effective.Add(2*time.Minute))
	// A non-unique database failure must not be misreported as a version
	// conflict. This trigger exists only inside this test's disposable schema.
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION reject_test_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic database failure' USING ERRCODE='23514'; END $$; CREATE TRIGGER reject_test_publication BEFORE INSERT ON addon_knowledge_publications FOR EACH ROW EXECUTE FUNCTION reject_test_publication()`); err != nil {
		t.Fatal(err)
	}
	if w := knowledgeRequest(h, actors["p"], "POST", "jobs/"+valid+"/publish", `{"expected_version":4}`); w.Code != 503 || !strings.Contains(w.Body.String(), `"operation_outcome_unknown"`) {
		t.Fatalf("non-unique database error misclassified: %d %s", w.Code, w.Body)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_test_publication ON addon_knowledge_publications`); err != nil {
		t.Fatal(err)
	}
	if w := knowledgeRequest(h, actors["p"], "POST", "jobs/"+valid+"/publish", `{"expected_version":4}`); w.Code != 200 {
		t.Fatalf("valid later publication: %d %s", w.Code, w.Body)
	}
}
