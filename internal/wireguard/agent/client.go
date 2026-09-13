package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/tsouza/runnerscout/internal/wireguard"
)

// Poll issues one GET against controllerURL's
// internal/health.WireGuardPeersHandler endpoint
// ("/v1/wireguard/peers/{allocationID}"), authenticated with pollToken as a
// bearer credential, and decodes the current authoritative peer snapshot
// for this allocation's NetworkProfile. It makes exactly one HTTP request
// and returns its own error for any failure (transport error, non-200
// status, malformed body) - this task's "plain, minimal poll loop" scope:
// no retry, no backoff; a caller on a fixed polling interval already gets
// "try again next interval" for free by calling Poll again next tick.
func Poll(ctx context.Context, client *http.Client, controllerURL, allocationID, pollToken string) ([]wireguard.Peer, error) {
	url := controllerURL + "/v1/wireguard/peers/" + allocationID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("agent: build poll request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+pollToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent: poll request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent: poll request returned status %d", resp.StatusCode)
	}
	var body struct {
		Peers []wireguard.Peer `json:"peers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("agent: decode poll response: %w", err)
	}
	return body.Peers, nil
}
