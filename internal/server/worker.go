package server

import (
	"context"
	"fmt"

	"github.com/ebnsina/yol/internal/jobs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// surveyWorker adapts the surveyor to the job runner. Kept here so the server package owns
// its own jobs and the jobs package stays free of any knowledge of servers.
type surveyWorker struct {
	river.WorkerDefaults[SurveyArgs]
	surveyor *Surveyor
}

func (w *surveyWorker) Work(ctx context.Context, job *river.Job[SurveyArgs]) error {
	return w.surveyor.Run(ctx, job.Args)
}

// RegisterWorkers adds this package's workers to the set.
func RegisterWorkers(workers *jobs.Workers, surveyor *Surveyor) {
	river.AddWorker(workers, &surveyWorker{surveyor: surveyor})
}

// JobEnqueuer starts server jobs. Inserting in the caller's transaction means a job is never
// queued for a server whose creation then rolled back.
type JobEnqueuer struct {
	runner *jobs.Runner
}

// NewEnqueuer builds the enqueuer.
func NewEnqueuer(runner *jobs.Runner) *JobEnqueuer {
	return &JobEnqueuer{runner: runner}
}

// EnqueueSurvey queues a look at a server. Someone is waiting on this, so it goes to the
// queue kept clear of slow background work.
func (e *JobEnqueuer) EnqueueSurvey(ctx context.Context, tx pgx.Tx, serverID, orgID uuid.UUID) error {
	_, err := e.runner.Client().InsertTx(ctx, tx,
		SurveyArgs{ServerID: serverID, OrgID: orgID},
		&river.InsertOpts{Queue: jobs.QueueInteractive},
	)
	if err != nil {
		return fmt.Errorf("server: queue survey: %w", err)
	}
	return nil
}
