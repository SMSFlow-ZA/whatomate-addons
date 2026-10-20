package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Receipt is server transport evidence, never a browser asserted delivery outcome.
// The configured verifier must authenticate ALL fields against its approved carrier
// account and cryptographic receipt or authenticated status-query response.
type Receipt struct {
	Tenant, Account, IntentID, ProviderID, ReceiptID, Evidence string
	ObservedAt                                                 time.Time
	Outcome                                                    string
}
type ReceiptVerifier func(context.Context, Receipt) error

func VerifyReceipt(ctx context.Context, r Receipt, verify ReceiptVerifier) (string, error) {
	if verify == nil || r.ObservedAt.IsZero() || (r.Outcome != "delivered" && r.Outcome != "failed") || r.Evidence == "" || len(r.Evidence) > 65536 {
		return "", ErrDenied
	}
	for _, v := range []string{r.Tenant, r.Account, r.IntentID, r.ProviderID, r.ReceiptID} {
		if v == "" || len(v) > 512 || strings.TrimSpace(v) != v || strings.ContainsRune(v, 0) {
			return "", ErrDenied
		}
	}
	if e := verify(ctx, r); e != nil {
		return "", ErrDenied
	}
	r.ObservedAt = r.ObservedAt.UTC()
	b, e := json.Marshal(r)
	if e != nil {
		return "", ErrDenied
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
