package health

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/wireguard"
)

// AllocationStore is the minimal read surface WireGuardPeersHandler needs
// from internal/state.Kubernetes: Load to find the polling allocation's own
// checkpointed poll-token hash, Phase and NetworkProfile; List to compute
// its current peer snapshot via wireguard.Snapshot. internal/state.Kubernetes
// already implements both methods with these exact signatures - this
// interface exists only so this handler's dependency surface is provably no
// wider than what it actually uses, not to add an adapter layer.
type AllocationStore interface {
	Load(ctx context.Context, id string) (lifecycle.Allocation, error)
	List(ctx context.Context) ([]lifecycle.Allocation, error)
}

// WireGuardPeersHandler implements the poll endpoint
// docs/networking-peer-model.md's "Revocation" section calls for: a narrow,
// allocation-scoped, bearer-token-authenticated way for a wireguard-mode
// allocation's VM to refresh its peer list after boot, since none of this
// codebase's three cloud providers can re-deliver cloud-init to a running
// instance. Mounted (when non-nil) at "GET /v1/wireguard/peers/{id}" by
// Status.Handler.
//
// # Auth mechanism
//
// The bearer token is per-allocation, not a shared secret across a
// NetworkProfile: a compromised VM's own token must only ever be able to
// read that one allocation's own peer list, never another allocation's -
// see docs/networking-peer-model.background.md's rejection of a shared
// Kubernetes ServiceAccount token for the same reasoning applied one layer
// up. The token is minted once per allocation
// (internal/wireguard.GeneratePollToken, called from
// provider.Command.CreateWithResources at the same Pending->Creating
// transition that mints the WireGuard keypair) and delivered once via
// cloud-init; the controller checkpoints only its SHA-256 hash
// (lifecycle.Allocation.WireGuardPollTokenHash), never the raw value - see
// that field's own doc comment for why (this codebase's checkpoint layer is
// a plain ConfigMap, not a Secret).
//
// A request is authorized only when all of the following hold, checked
// against a freshly loaded Allocation on every single request (no caching):
// the Authorization header is exactly "Bearer <hex>"; the path's allocation
// ID resolves to a real, checkpointed allocation; that allocation has a
// checkpointed poll-token hash of the expected size (i.e. it is, or once
// was, a wireguard-mode allocation); its SHA-256-hashed presented token
// matches that checkpointed hash, compared with crypto/subtle.ConstantTimeCompare;
// and its Phase is not Deleted or TimedOut (docs/networking-peer-model.background.md:
// "Its own poll token stops working the moment the controller marks the
// allocation Deleted/TimedOut").
//
// Any failure - missing header, malformed header, wrong token, a real
// token for a *different* allocation, or an allocation ID that does not
// exist - returns the identical 401 Unauthorized with an empty body: 401 is
// the correct status for a bad/missing credential (RFC 7235), as opposed to
// 403, which would imply an otherwise-identified caller lacking permission -
// there is no identity here independent of the token itself. The body and
// status are deliberately identical across every failure mode so a
// requester cannot use this endpoint to enumerate valid allocation IDs or
// distinguish "wrong token" from "no such allocation".
//
// # Residual timing risk
//
// crypto/subtle.ConstantTimeCompare makes the byte-for-byte hash comparison
// itself constant-time, but two real timing side-channels remain
// undefended, deliberately, rather than silently: (1) Load is a network call
// to the Kubernetes API server whose latency swamps any nanosecond-scale
// comparison difference and varies for reasons entirely unrelated to
// whether the ID exists (etcd load, network jitter); closing that gap would
// require padding every response to a fixed artificial latency, which this
// first slice does not attempt. (2) extractBearerHash below returns
// immediately, without hashing anything, when the header does not even have
// the "Bearer " prefix - a real but sub-microsecond difference from the
// hashed-but-wrong-value path, and one that reveals only whether the caller
// formatted the header correctly, not anything about a correct token's
// bytes.
//
// # Rate limiting / abuse protection
//
// No rate limiter, token bucket, or per-source-IP throttle is added. This
// endpoint is already the simplest shape that resists cheap abuse: it is
// read-only, does exactly one Load (and, only on success, one List) against
// the existing Kubernetes-backed store per request, and its response size is
// bounded by the real number of allocations in one NetworkProfile - never by
// anything an unauthenticated caller controls. A failed auth attempt costs
// one Load and nothing else. Status.WireGuardPeers now mounts a real,
// non-nil instance of this handler at both cmd/runnerscout/main.go entry
// points, but it still has zero real traffic until an operator actually
// configures a "wireguard" mode NetworkProfile (see
// internal/configapi/compile.go's network()) - the concrete threat this
// leaves open is a compromised VM polling far faster than any intended
// interval and generating a correspondingly higher rate of List calls
// against the Kubernetes API server than a cooperative poller would - the
// natural fix then would be a minimum per-allocation poll interval enforced
// server-side (e.g. 429 on a too-recent repeat poll), deliberately not
// built here since it would be speculative machinery ahead of any observed
// real traffic pattern.
type WireGuardPeersHandler struct {
	Store AllocationStore
}

// decoyHash is compared against, in constant time, whenever the request's
// allocation ID does not resolve to a real, currently-eligible allocation -
// so "no such allocation" takes exactly the same comparison shape as "wrong
// token for a real allocation" rather than skipping the comparison
// altogether. It is simply the zero value: no real poll-token hash can
// equal it except with probability 2^-256, so its only role is shape
// uniformity, not secrecy.
var decoyHash [sha256.Size]byte

func (h *WireGuardPeersHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	presented, tokenOK := extractBearerHash(r.Header.Get("Authorization"))

	allocation, loadErr := h.Store.Load(r.Context(), id)
	expected := decoyHash
	eligible := loadErr == nil &&
		len(allocation.WireGuardPollTokenHash) == sha256.Size &&
		allocation.Phase != lifecycle.Deleted &&
		allocation.Phase != lifecycle.TimedOut
	if eligible {
		copy(expected[:], allocation.WireGuardPollTokenHash)
	}

	// Computed as its own statement, unconditionally, rather than as the
	// last operand of a short-circuited `||` below: Go's || stops
	// evaluating once an earlier operand is true, so folding this call in
	// as a third clause would skip it entirely whenever !tokenOK or
	// !eligible already was true - contradicting decoyHash's own doc
	// comment above, which promises the comparison always runs so an
	// ineligible allocation takes the same comparison shape as a wrong
	// token for a real one, rather than skipping the comparison outright.
	match := subtle.ConstantTimeCompare(presented[:], expected[:]) == 1
	if !tokenOK || !eligible || !match {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	allocations, err := h.Store.List(r.Context())
	if err != nil {
		// A failure here happens only after the caller already proved its
		// identity - it is an operational error, not an auth failure, so it
		// must not be folded into the same 401 the auth checks above use.
		http.Error(w, "peer snapshot unavailable", http.StatusInternalServerError)
		return
	}
	peers := wireguard.Snapshot(id, allocation.NetworkProfile, allocations)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		Peers []wireguard.Peer `json:"peers"`
	}{Peers: peers})
}

// extractBearerHash parses "Bearer <hex>" out of an Authorization header
// value and returns the SHA-256 hash of the decoded token bytes. ok is false
// for any header that is missing, uses a different scheme, or whose token
// is not valid hex - all treated identically by the caller.
func extractBearerHash(header string) (hash [sha256.Size]byte, ok bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return hash, false
	}
	token := strings.TrimPrefix(header, prefix)
	computed, err := wireguard.HashPollToken(token)
	if err != nil {
		return hash, false
	}
	return computed, true
}
