package githubjobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing or wrong bearer token")
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("missing GitHub Accept header")
		}
		if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Error("missing GitHub API version header")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return &Client{HTTPClient: server.Client(), BaseURL: server.URL, Token: "fixture-token"}
}
func writeJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }

func TestAttemptJobsParsesRESTIdentity(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/repos/acme/widgets/actions/runs/99/attempts/2/jobs" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, map[string]any{"total_count": 1, "jobs": []any{
			map[string]any{"id": 345, "run_id": 99, "run_attempt": 2, "runner_name": "rs-a", "status": "completed", "conclusion": "failure"},
		}})
	})
	jobs, err := c.AttemptJobs(context.Background(), "acme", "widgets", 99, 2)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	want := jobs[0]
	if want.ID != 345 || want.RunID != 99 || want.Attempt != 2 || want.RunnerName != "rs-a" || want.Status != "completed" || want.Conclusion != "failure" {
		t.Fatal("job fields not mapped correctly", want)
	}
}
func TestAttemptJobsRejectsNonOKStatus(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		writeJSON(w, map[string]any{"message": "secret diagnostic"})
	})
	if _, err := c.AttemptJobs(context.Background(), "acme", "widgets", 99, 2); err == nil {
		t.Fatal("expected error for non-200 response")
	}
}
func TestAttemptJobsReports404AsErrAttemptNotFound(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		writeJSON(w, map[string]any{"message": "Not Found"})
	})
	_, err := c.AttemptJobs(context.Background(), "acme", "widgets", 99, 7)
	if !errors.Is(err, ErrAttemptNotFound) {
		t.Fatal("expected a 404 response to report ErrAttemptNotFound", err)
	}
}
func TestAttemptJobsRejectsMalformedBody(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	if _, err := c.AttemptJobs(context.Background(), "acme", "widgets", 99, 2); err == nil {
		t.Fatal("expected error for malformed body")
	}
}
func TestRerunFailedJobsPostsToCorrectPath(t *testing.T) {
	var posted bool
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/repos/acme/widgets/actions/runs/99/rerun-failed-jobs" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		posted = true
		w.WriteHeader(201)
	})
	if err := c.RerunFailedJobs(context.Background(), "acme", "widgets", 99); err != nil || !posted {
		t.Fatal("rerun not posted correctly", err, posted)
	}
}
func TestRerunFailedJobsRejectsNonCreatedStatus(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		writeJSON(w, map[string]any{"message": "no failed jobs"})
	})
	if err := c.RerunFailedJobs(context.Background(), "acme", "widgets", 99); err == nil {
		t.Fatal("expected error for non-201 response")
	}
}
