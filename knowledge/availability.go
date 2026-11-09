package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
)

// ReferenceAvailable returns only a current-authority boolean. It never exposes
// document metadata or text to callers that cannot retrieve the source.
func (r *PostgresRepository) ReferenceAvailable(ctx context.Context, q Query, ref Source, auth Authorizer) (bool, error) {
	if auth == nil || q.At.IsZero() || !auth.Allowed(ctx, q.Principal, q.Audience) {
		return false, ErrDenied
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return false, e
	}
	defer r.rollback(tx)
	if e = r.authorize(ctx, tx, q.Principal, "read", q.Audience); e != nil {
		return false, e
	}
	var dRaw, eRaw, aRaw []byte
	var manifest string
	var retired bool
	var withdrawn bool
	e = tx.QueryRowContext(ctx, `SELECT document,evidence,approval,manifest_hash,retired,withdraw_at IS NOT NULL AND withdraw_at<=$3 FROM addon_knowledge_publications WHERE tenant=$1 AND document_id=$2 AND effective_at<=$3 ORDER BY effective_at DESC LIMIT 1`, r.tenant, ref.DocumentID, q.At).Scan(&dRaw, &eRaw, &aRaw, &manifest, &retired, &withdrawn)
	if e != nil || retired || withdrawn {
		return false, e
	}
	var d Document
	var evidence Evidence
	var approval Approval
	if json.Unmarshal(dRaw, &d) != nil || json.Unmarshal(eRaw, &evidence) != nil || json.Unmarshal(aRaw, &approval) != nil {
		return false, ErrDenied
	}
	publication, e := Publish(d, evidence, approval)
	if e != nil || publication.manifestHash != manifest {
		return false, ErrDenied
	}
	allowed := false
	for _, a := range d.Audiences {
		if a == q.Audience {
			allowed = true
		}
	}
	return EqualPublicReference(d.PublicReference, ref.PublicReference) && allowed && d.ID == ref.DocumentID && d.Version == ref.Version && manifest == ref.ManifestHash && ref.Page.Number > 0 && ref.Page.Number <= len(d.Pages) && ref.Reference == fmt.Sprintf("%s:%d", manifest, ref.Page.Number), nil
}
