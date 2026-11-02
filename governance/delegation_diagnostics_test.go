package governance

import (
	"database/sql"
	"errors"
	"testing"
)

func TestDelegationDenialPreservesCauseWithoutExposingDetails(t *testing.T) {
	for _, cause := range []error{sql.ErrConnDone, ErrDelegationExpired, errors.New("private database detail")} {
		e := delegationDenied(cause)
		if !errors.Is(e, ErrDenied) || !errors.Is(e, cause) || e.Error() != ErrDenied.Error() {
			t.Fatal("typed cause lost or raw details exposed")
		}
	}
}
