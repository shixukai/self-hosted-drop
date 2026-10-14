package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shixukai/self-hosted-drop/internal/archive"
)

func (a *App) autoPublish(ctx context.Context, d Deployment, report archive.Result) error {
	if d.AutoPublicationID != nil || d.AutoPublishErrorCode != "" {
		return nil
	}
	key := "auto-" + d.ID
	// Acceptance and result recording use separate transactions. Recover an
	// already accepted request before rechecking current token permissions, so a
	// result-write retry cannot turn a publication into a revoked-token refusal.
	var acceptedID string
	err := a.DB.QueryRow(ctx, "SELECT p.id FROM idempotency_records i JOIN publications p ON p.id=i.resource_id WHERE i.actor_id=$1 AND i.operation=$2 AND i.key=$3", d.CreatedBy, "publication:"+d.SiteID, key).Scan(&acceptedID)
	if err == nil {
		return a.recordAutoPublication(ctx, d, &acceptedID, nil)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	u := Actor{ID: d.CreatedBy, Admin: !strings.HasPrefix(d.CreatedBy, "token:")}
	if !u.Admin {
		u.TokenID = strings.TrimPrefix(d.CreatedBy, "token:")
		if err = a.DB.QueryRow(ctx, "SELECT scopes FROM api_tokens WHERE id=$1 AND revoked_at IS NULL AND expires_at>now()", u.TokenID).Scan(&u.Scopes); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			return a.recordAutoPublication(ctx, d, nil, apiError(http.StatusUnauthorized, "TOKEN_REVOKED", "上传使用的 Token 已过期或撤销"))
		}
	}
	r, err := http.NewRequestWithContext(context.WithValue(ctx, actorKey{}, u), http.MethodPost, "http://internal", nil)
	if err != nil {
		return err
	}
	r.Header.Set("Idempotency-Key", key)
	if err = a.require(r, "publications:write", d.SiteID); err != nil {
		return a.autoPublicationError(ctx, d, err)
	}
	p, err := a.acceptPublication(r, d.SiteID, publishRequest{DeploymentID: d.ID, ExpectedGeneration: d.Metadata.ExpectedGeneration, Action: "PUBLISH", ReportHash: report.ReportHash, AcknowledgeWarnings: d.Metadata.WarningsPolicy == "allow"})
	if err != nil {
		return a.autoPublicationError(ctx, d, err)
	}
	return a.recordAutoPublication(ctx, d, &p.ID, nil)
}

func (a *App) autoPublicationError(ctx context.Context, d Deployment, err error) error {
	var refusal *APIError
	if !errors.As(err, &refusal) || refusal.Status >= http.StatusInternalServerError || refusal.Retryable {
		return err
	}
	return a.recordAutoPublication(ctx, d, nil, refusal)
}

func (a *App) recordAutoPublication(ctx context.Context, d Deployment, publicationID *string, refusal *APIError) error {
	code, message, result := "", "", "ACCEPTED"
	if refusal != nil {
		code, message, result = refusal.Code, refusal.Message, refusal.Code
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	changed, err := tx.Exec(ctx, "UPDATE deployments SET auto_publication_id=$2,auto_publish_error_code=$3,auto_publish_error_message=$4,updated_at=now() WHERE id=$1 AND auto_publication_id IS NULL AND auto_publish_error_code=''", d.ID, publicationID, code, message)
	if err != nil {
		return err
	}
	if changed.RowsAffected() > 0 {
		if _, err = tx.Exec(ctx, "INSERT INTO audit_events(actor,action,site_id,resource_id,result) VALUES($1,'AUTO_PUBLISH',$2,$3,$4)", d.CreatedBy, d.SiteID, d.ID, result); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
