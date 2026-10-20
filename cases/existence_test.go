package cases

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestChangeStateDoesNotDiscloseUnknownCaseToUnassignedActor(t *testing.T) {
	service := Service{Repository: NewMemoryRepository()}
	_, err := service.ChangeState(context.Background(), Principal{ID: "unassigned"}, "unknown", 1, InProgress, "", time.Now())
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("case existence exposed: %v", err)
	}
}
