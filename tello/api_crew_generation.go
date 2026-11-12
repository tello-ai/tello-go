package tello

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	defaultGenerationPollInterval = 2 * time.Second
	defaultGenerationTimeout      = 600 * time.Second
)

// CrewGenerations reads crew generation jobs started by Crews.Generate.
type CrewGenerations struct {
	t *apiTransport
}

// Get returns a crew generation job:
// {"id", "status": "running"|"succeeded"|"failed", "goal", "crew", "error", ...}.
// A succeeded job has the crew; a failed one its error and possibly the crew
// it left behind.
func (r *CrewGenerations) Get(ctx context.Context, id string) (Object, error) {
	return r.t.getItem(ctx, "crew-generations", id)
}

// Generate starts building, deploying and publishing a crew from input
// ({"goal": ...} and the other fields the docs list) and returns the job,
// usually {"id", "status": "running", "goal"}. The account builds one crew
// at a time: the same goal while it runs, or within 10 minutes of its
// success, returns that job instead of a new one; another goal fails with
// crew_generation_in_progress and the running job's id in Data["id"].
// WithIdempotencyKey makes the request safe to resend.
func (r *Crews) Generate(ctx context.Context, input Object, opts ...RequestOption) (Object, error) {
	return r.generate(ctx, input, collectRequestOptions(opts).idempotencyKey, nil)
}

func (r *Crews) generate(ctx context.Context, input Object, idempotencyKey string, require []string) (Object, error) {
	return r.t.do(ctx, apiRequest{
		method: http.MethodPost, path: "/crews/generate", body: orEmpty(input),
		idempotencyKey: idempotencyKey, long: true,
		accept:  []int{http.StatusAccepted, http.StatusOK},
		require: require,
	})
}

// GenerateAndWaitOptions configures Crews.GenerateAndWait. Zero values take
// the defaults.
type GenerateAndWaitOptions struct {
	// PollInterval is the pause between job reads (default 2s).
	PollInterval time.Duration
	// Timeout bounds the whole wait, generate request included (default
	// 600s).
	Timeout time.Duration
	// IdempotencyKey is sent with the generate request when non-empty.
	IdempotencyKey string
}

func (o GenerateAndWaitOptions) withDefaults() GenerateAndWaitOptions {
	if o.PollInterval <= 0 {
		o.PollInterval = defaultGenerationPollInterval
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultGenerationTimeout
	}
	return o
}

// GenerateAndWait runs Generate, then reads the job every PollInterval until
// its status is no longer "running", and returns that job. A failed job is
// returned, not an error: branch on job["status"]. When Timeout passes first
// it returns a *CrewGenerationTimeoutError with the job id; the job keeps
// running and CrewGenerations.Get can follow it. Errors of Generate (such as
// crew_generation_in_progress) are returned as they are; an error reading the
// job is returned wrapped with the job id.
func (r *Crews) GenerateAndWait(ctx context.Context, input Object, opts GenerateAndWaitOptions) (Object, error) {
	opts = opts.withDefaults()
	deadline := time.Now().Add(opts.Timeout)
	job, err := r.generate(ctx, input, opts.IdempotencyKey, []string{"id", "status"})
	if err != nil {
		return nil, err
	}
	id := job["id"].(string)
	poll := apiRequest{
		method: http.MethodGet, path: "/crew-generations/" + url.PathEscape(id),
		accept: []int{http.StatusOK}, require: []string{"status"},
	}
	for job["status"] == "running" {
		wait := min(opts.PollInterval, time.Until(deadline))
		if wait <= 0 {
			return nil, &CrewGenerationTimeoutError{
				TelloError: TelloError{Message: fmt.Sprintf("tello api: crew generation %s still running after %v", id, opts.Timeout)},
				JobID:      id,
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("tello api: wait for crew generation %s: %w", id, ctx.Err())
		case <-timer.C:
		}
		if job, err = r.t.do(ctx, poll); err != nil {
			return nil, fmt.Errorf("tello api: read crew generation %s: %w", id, err)
		}
	}
	return job, nil
}
