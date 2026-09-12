package health

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestReadinessDoesNotTurnDependencyFailureIntoLivenessFailure(t *testing.T) {
	var status Status
	server := httptest.NewServer(status.Handler())
	defer server.Close()
	check := func(path string, code int) {
		t.Helper()
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != code {
			t.Fatalf("%s status %d, want %d", path, resp.StatusCode, code)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil || len(body) > 32 {
			t.Fatalf("unexpected health response %q %v", body, err)
		}
	}
	check("/readyz", 503)
	check("/healthz", 200)
	status.SetReady(true)
	check("/readyz", 200)
	status.SetReady(false)
	check("/readyz", 503)
	check("/healthz", 200)
	check("/not-a-probe", 404)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				status.SetReady(j%2 == 0)
				req := httptest.NewRequest("GET", "/readyz", nil)
				status.Handler().ServeHTTP(httptest.NewRecorder(), req)
			}
		}()
	}
	wg.Wait()
}
