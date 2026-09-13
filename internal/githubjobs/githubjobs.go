// Package githubjobs fetches a workflow run's attempt jobs and requests a
// rerun of its failed jobs, giving internal/recovery's policy guard real
// REST evidence. It never assigns a request to a scale-set job identity;
// callers own that pairing.
package githubjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/tsouza/runnerscout/internal/recovery"
)

// Client is a minimal REST client for the workflow-run jobs and rerun
// endpoints, authenticated with a personal access token. BaseURL defaults to
// the public GitHub API; tests override it with a local fixture server.
type Client struct {
	HTTPClient *http.Client
	BaseURL    string
	Token      string
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.github.com"
}
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}
func (c *Client) do(ctx context.Context, method, path string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return c.httpClient().Do(request)
}

type attemptJob struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	RunAttempt int    `json:"run_attempt"`
	RunnerName string `json:"runner_name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}
type attemptJobsResponse struct {
	Jobs []attemptJob `json:"jobs"`
}

// AttemptJobs fetches the jobs recorded for one attempt of one workflow run.
func (c *Client) AttemptJobs(ctx context.Context, owner, repo string, runID int64, attempt int) ([]recovery.RESTJob, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/attempts/%d/jobs", owner, repo, runID, attempt)
	response, err := c.do(ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub attempt jobs request failed with status %d", response.StatusCode)
	}
	var decoded attemptJobsResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, errors.New("GitHub attempt jobs response malformed")
	}
	jobs := make([]recovery.RESTJob, 0, len(decoded.Jobs))
	for _, job := range decoded.Jobs {
		jobs = append(jobs, recovery.RESTJob{ID: job.ID, RunID: job.RunID, Attempt: job.RunAttempt, RunnerName: job.RunnerName, Status: job.Status, Conclusion: job.Conclusion})
	}
	return jobs, nil
}

// RerunFailedJobs requests GitHub rerun only the failed jobs of a workflow run.
func (c *Client) RerunFailedJobs(ctx context.Context, owner, repo string, runID int64) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/rerun-failed-jobs", owner, repo, runID)
	response, err := c.do(ctx, http.MethodPost, path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("GitHub rerun-failed-jobs request failed with status %d", response.StatusCode)
	}
	return nil
}
