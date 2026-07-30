package server

import (
	"context"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// recordEvent appends a step to a server's progress. Messages are written for the person
// watching, not for a log file, because this is what they see while they wait.
func recordEvent(ctx context.Context, q *sqlc.Queries, orgID, serverID uuid.UUID, step, message, level string) error {
	_, err := q.RecordServerEvent(ctx, sqlc.RecordServerEventParams{
		ID:       uuid.New(),
		OrgID:    orgID,
		ServerID: serverID,
		Step:     step,
		Message:  message,
		Level:    level,
	})
	if err != nil {
		return httpx.Internal(err)
	}
	return nil
}

func toTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func timeOrNil(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	value := ts.Time
	return &value
}
