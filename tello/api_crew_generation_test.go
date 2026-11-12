package tello

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

const (
	runningJob   = `{"id":"57","status":"running","goal":"g"}`
	succeededJob = `{"id":"57","status":"succeeded","goal":"g","crew":{"id":"35"},"error":null}`
	failedJob    = `{"id":"57","status":"failed","goal":"g","crew":null,"error":{"code":"crew_generation_failed","message":"m"}}`
)

func TestCrewsGenerate(t *testing.T) {
	for _, status := range []int{http.StatusAccepted, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newFakeAPI(t, reply(status, runningJob))
			api := newTestAPI(t, f)

			job, err := api.Crews.Generate(context.Background(), Object{"goal": "g", "extra": true})
			if err != nil {
				t.Fatal(err)
			}
			if job["id"] != "57" || job["status"] != "running" {
				t.Fatalf("job = %v", job)
			}
			req := f.only(t)
			if req.Method != http.MethodPost || req.Path != "/v1/crews/generate" {
				t.Fatalf("request = %s %s", req.Method, req.Path)
			}
			if body := decodeBody(t, req); body["goal"] != "g" || body["extra"] != true {
				t.Fatalf("body = %v", body)
			}
			if _, sent := req.Header["Idempotency-Key"]; sent {
				t.Fatal("sent an Idempotency-Key nobody asked for")
			}
		})
	}
}

func TestCrewsGenerateSendsGivenIdempotencyKey(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusAccepted, runningJob))
	api := newTestAPI(t, f)

	if _, err := api.Crews.Generate(context.Background(), Object{"goal": "g"}, WithIdempotencyKey("gen-1")); err != nil {
		t.Fatal(err)
	}
	if got := f.only(t).Header.Get("Idempotency-Key"); got != "gen-1" {
		t.Fatalf("Idempotency-Key = %q", got)
	}
}

func TestCrewsGenerateRefusesUndocumentedSuccessStatus(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, runningJob))
	api := newTestAPI(t, f)

	_, err := api.Crews.Generate(context.Background(), Object{"goal": "g"})

	if apiErr := asAPIError(t, err); apiErr.Code != "invalid_response" {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestCrewsGenerateInProgressCarriesRunningJobID(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusConflict,
		`{"statusCode":409,"message":"A crew generation is already running","error":"crew_generation_in_progress","data":{"id":"56"}}`))
	api := newTestAPI(t, f)

	_, err := api.Crews.Generate(context.Background(), Object{"goal": "g"})

	apiErr := asAPIError(t, err)
	if apiErr.Code != "crew_generation_in_progress" || apiErr.Data["id"] != "56" {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestCrewGenerationsGet(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, succeededJob))
	api := newTestAPI(t, f)

	job, err := api.CrewGenerations.Get(context.Background(), "57")
	if err != nil {
		t.Fatal(err)
	}
	if job["status"] != "succeeded" || job["crew"].(map[string]any)["id"] != "35" {
		t.Fatalf("job = %v", job)
	}
	req := f.only(t)
	if req.Method != http.MethodGet || req.Path != "/v1/crew-generations/57" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
}

func TestCrewGenerationsGetNotFound(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusNotFound,
		`{"statusCode":404,"message":"Crew generation not found","error":"crew_generation_not_found","data":null}`))
	api := newTestAPI(t, f)

	_, err := api.CrewGenerations.Get(context.Background(), "999999")

	if apiErr := asAPIError(t, err); apiErr.Status != 404 || apiErr.Code != "crew_generation_not_found" {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestCrewGenerationsGetRefusesEmptyIDWithoutRequest(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, succeededJob))
	api := newTestAPI(t, f)

	if _, err := api.CrewGenerations.Get(context.Background(), ""); err == nil {
		t.Fatal("expected an error")
	}
	if n := len(f.all()); n != 0 {
		t.Fatalf("expected no request, got %d", n)
	}
}

// jobSequence answers the generate request and then each poll with the next
// body; the last one repeats.
func jobSequence(bodies ...string) func(int, fakeRequest) fakeResponse {
	return func(n int, _ fakeRequest) fakeResponse {
		status := http.StatusOK
		if n == 0 {
			status = http.StatusAccepted
		}
		return fakeResponse{status: status, body: bodies[min(n, len(bodies)-1)]}
	}
}

func TestCrewsGenerateAndWaitReturnsSucceededJob(t *testing.T) {
	f := newFakeAPI(t, jobSequence(runningJob, runningJob, succeededJob))
	api := newTestAPI(t, f)

	job, err := api.Crews.GenerateAndWait(context.Background(), Object{"goal": "g"},
		GenerateAndWaitOptions{PollInterval: 10 * time.Millisecond, IdempotencyKey: "gen-2"})
	if err != nil {
		t.Fatal(err)
	}
	if job["status"] != "succeeded" {
		t.Fatalf("job = %v", job)
	}
	reqs := f.all()
	if len(reqs) != 3 {
		t.Fatalf("expected generate + 2 polls, got %d requests", len(reqs))
	}
	if reqs[0].Path != "/v1/crews/generate" || reqs[0].Header.Get("Idempotency-Key") != "gen-2" {
		t.Fatalf("first request = %s key %q", reqs[0].Path, reqs[0].Header.Get("Idempotency-Key"))
	}
	for _, poll := range reqs[1:] {
		if poll.Method != http.MethodGet || poll.Path != "/v1/crew-generations/57" {
			t.Fatalf("poll = %s %s", poll.Method, poll.Path)
		}
		if _, sent := poll.Header["Idempotency-Key"]; sent {
			t.Fatal("a poll sent an Idempotency-Key")
		}
	}
	if gap := reqs[2].ReceivedAt.Sub(reqs[1].ReceivedAt); gap < 10*time.Millisecond {
		t.Fatalf("polled %v apart, want at least the poll interval", gap)
	}
}

func TestCrewsGenerateAndWaitDoesNotPollAFinishedJob(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, succeededJob))
	api := newTestAPI(t, f)

	job, err := api.Crews.GenerateAndWait(context.Background(), Object{"goal": "g"}, GenerateAndWaitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job["status"] != "succeeded" || len(f.all()) != 1 {
		t.Fatalf("job = %v after %d requests", job, len(f.all()))
	}
}

func TestCrewsGenerateAndWaitReturnsFailedJobWithoutError(t *testing.T) {
	f := newFakeAPI(t, jobSequence(runningJob, failedJob))
	api := newTestAPI(t, f)

	job, err := api.Crews.GenerateAndWait(context.Background(), Object{"goal": "g"},
		GenerateAndWaitOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("a failed job is not an error: %v", err)
	}
	if job["status"] != "failed" || job["error"].(map[string]any)["code"] != "crew_generation_failed" {
		t.Fatalf("job = %v", job)
	}
}

func TestCrewsGenerateAndWaitTimesOutWithJobID(t *testing.T) {
	f := newFakeAPI(t, jobSequence(runningJob))
	api := newTestAPI(t, f)

	start := time.Now()
	_, err := api.Crews.GenerateAndWait(context.Background(), Object{"goal": "g"},
		GenerateAndWaitOptions{PollInterval: 10 * time.Millisecond, Timeout: 60 * time.Millisecond})

	var timeout *CrewGenerationTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("expected *CrewGenerationTimeoutError, got %T: %v", err, err)
	}
	if timeout.JobID != "57" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %+v", timeout)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("gave up after %v", elapsed)
	}
	if n := len(f.all()); n < 3 {
		t.Fatalf("expected several polls, got %d requests", n)
	}
}

func TestCrewsGenerateAndWaitReturnsInProgressError(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusConflict,
		`{"statusCode":409,"message":"A crew generation is already running","error":"crew_generation_in_progress","data":{"id":"56"}}`))
	api := newTestAPI(t, f)

	_, err := api.Crews.GenerateAndWait(context.Background(), Object{"goal": "g"}, GenerateAndWaitOptions{})

	apiErr := asAPIError(t, err)
	if apiErr.Code != "crew_generation_in_progress" || apiErr.Data["id"] != "56" {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestCrewsGenerateAndWaitStopsWhenContextEnds(t *testing.T) {
	f := newFakeAPI(t, jobSequence(runningJob))
	api := newTestAPI(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)

	_, err := api.Crews.GenerateAndWait(ctx, Object{"goal": "g"}, GenerateAndWaitOptions{PollInterval: time.Hour})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	var timeout *CrewGenerationTimeoutError
	if errors.As(err, &timeout) {
		t.Fatal("cancellation is not the wait timeout")
	}
}

func TestCrewsGenerateAndWaitRefusesJobWithoutID(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusAccepted, `{"status":"running"}`))
	api := newTestAPI(t, f)

	_, err := api.Crews.GenerateAndWait(context.Background(), Object{"goal": "g"}, GenerateAndWaitOptions{})

	if apiErr := asAPIError(t, err); apiErr.Code != "invalid_response" {
		t.Fatalf("error = %+v", apiErr)
	}
	if n := len(f.all()); n != 1 {
		t.Fatalf("expected no poll, got %d requests", n)
	}
}

func TestGenerateAndWaitOptionsDefaults(t *testing.T) {
	got := GenerateAndWaitOptions{}.withDefaults()
	if got.PollInterval != 2*time.Second || got.Timeout != 600*time.Second {
		t.Fatalf("defaults = %+v", got)
	}
}
