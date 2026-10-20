//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"net/url"
	"os"
	"sync"
	"testing"
)

func TestConcurrentFirstInstallMigrations(t *testing.T) {
	dsn := os.Getenv("WORKFLOW_DATABASE_URL")
	if dsn == "" {
		t.Skip("WORKFLOW_DATABASE_URL required")
	}
	ctx := context.Background()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	for _, module := range []string{"directory", "cases", "governance"} {
		t.Run(module, func(t *testing.T) {
			var token [12]byte
			if _, err := rand.Read(token[:]); err != nil {
				t.Fatal(err)
			}
			schema := "addon_migration_test_" + hex.EncodeToString(token[:])
			if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
				t.Fatal(err)
			}
			defer base.ExecContext(ctx, `DROP SCHEMA "`+schema+`" CASCADE`)
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			db, err := sql.Open("pgx", u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var migrate func(context.Context) error
			switch module {
			case "directory":
				store, err := directory.NewPostgresStore(db, "org", "actor")
				if err != nil {
					t.Fatal(err)
				}
				migrate = store.Init
			case "cases":
				migrate = (&cases.SQLRepository{DB: db}).Migrate
			case "governance":
				migrate = (&governance.SQLRepository{DB: db}).Migrate
			}
			start := make(chan struct{})
			failures := make(chan error, 8)
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if err := migrate(ctx); err != nil {
						failures <- err
					}
				}()
			}
			close(start)
			wg.Wait()
			close(failures)
			for err := range failures {
				t.Error(err)
			}
		})
	}
}
