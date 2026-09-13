// Package wireguard mints per-allocation WireGuard Curve25519 identities and
// computes the peer snapshot a NetworkProfile's allocations embed into their
// own cloud-init payload, per docs/networking-peer-model.md's "Peer trust"
// section. It implements only that piece: no handshake, no poll endpoint, no
// revocation, no data-plane device. Nothing in this repository calls it yet -
// internal/configapi/compile.go's network() still rejects every
// NetworkProfileSpec.Mode other than "separate", so no allocation produced by
// this codebase's own configuration path can ever set
// lifecycle.Allocation.NetworkProfile, the field that would make any of this
// reachable.
package wireguard

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"golang.org/x/crypto/curve25519"

	"github.com/tsouza/runnerscout/internal/lifecycle"
)

// KeySize is the byte length of a WireGuard Curve25519 key, private or public.
const KeySize = 32

// PrivateKey is a WireGuard Curve25519 private key. It intentionally carries
// no JSON struct tag anywhere, and never will: encoding/json marshals every
// exported field by its Go name whether or not that field has a tag, so
// omitting a tag alone would NOT stop this type from being serialized if it
// were ever embedded in some future struct someone marshals (a debug dump, an
// errant log payload, ...). MarshalJSON below closes that gap structurally
// instead of relying on every future call site remembering a `json:"-"` tag:
// it fails unconditionally, so any accidental encoding/json.Marshal of a
// value containing a PrivateKey errors loudly instead of writing key
// material. String and GoString apply the same redaction to fmt's
// %v/%+v and %#v verbs, mirroring the credential-redaction pattern already
// used by internal/provider/aws_sdk.go's AWSSDK.String/GoString and
// internal/prices/aws.go's AWSSpotClient.String/GoString.
type PrivateKey [KeySize]byte

func (PrivateKey) String() string   { return "wireguard.PrivateKey(REDACTED)" }
func (PrivateKey) GoString() string { return "wireguard.PrivateKey(REDACTED)" }

// MarshalJSON always fails: a WireGuard private key must never be persisted
// or transmitted as JSON. It is generated in controller memory and consumed
// once into cloud-init (docs/networking-peer-model.md's "Peer trust" and
// "Secret shape" sections) - nothing about it is ever checkpointed.
func (PrivateKey) MarshalJSON() ([]byte, error) {
	return nil, errors.New("wireguard: refusing to marshal a private key to JSON")
}

// Base64 returns the standard base64 encoding of the raw key bytes - the same
// encoding `wg genkey`/`wg pubkey` and every other WireGuard tool use, and
// the only supported way to extract raw bytes from a PrivateKey.
func (k PrivateKey) Base64() string { return base64.StdEncoding.EncodeToString(k[:]) }

// PublicKey is a WireGuard Curve25519 public key. It is not secret: safe to
// log, checkpoint on lifecycle.Allocation, and hand to every peer.
type PublicKey [KeySize]byte

func (k PublicKey) String() string { return base64.StdEncoding.EncodeToString(k[:]) }

// Bytes returns a copy of the raw key bytes, the form
// lifecycle.Allocation.WireGuardPublicKey and Peer.PublicKey both use.
func (k PublicKey) Bytes() []byte { return append([]byte(nil), k[:]...) }

// KeyPair is the private/public identity minted for one allocation at its
// Pending->Creating transition (docs/networking-peer-model.md's "Peer trust"
// section).
type KeyPair struct {
	Private PrivateKey
	Public  PublicKey
}

// Generate mints a fresh WireGuard keypair using crypto/rand as the entropy
// source. It never logs, prints or otherwise exposes the private key outside
// this return value.
//
// golang.zx2c4.com/wireguard/device is the library this repository's
// docs/networking-control-plane.md names as the eventual data-plane
// dependency, but `go doc golang.zx2c4.com/wireguard/device` shows its own
// key generation (newPrivateKey, (*NoisePrivateKey).publicKey) is
// unexported - the module exposes no public key-generation API at all, only
// a Device that expects an already-generated key. This function instead
// implements the same well-documented Curve25519 clamp (RFC 7748, the
// convention `wg genkey` itself follows) directly against
// golang.org/x/crypto/curve25519, already an indirect dependency of this
// module and promoted to direct by this package's import - no new module is
// added to go.mod for something the wireguard module itself does not export.
func Generate() (KeyPair, error) {
	var priv PrivateKey
	if _, err := rand.Read(priv[:]); err != nil {
		return KeyPair{}, fmt.Errorf("wireguard: generate private key: %w", err)
	}
	// Clamp: clear the low 3 bits, clear the high bit and set bit 254 - the
	// standard Curve25519 scalar clamp. golang.org/x/crypto/curve25519's
	// X25519 (backed by crypto/ecdh since the version this module vendors)
	// already clamps internally before multiplying, so this is redundant for
	// the resulting point; it is done anyway so the stored private key bytes
	// are themselves the canonical clamped scalar every other WireGuard
	// implementation produces and expects. Clamping is idempotent, so
	// applying it twice changes nothing.
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return KeyPair{}, fmt.Errorf("wireguard: derive public key: %w", err)
	}
	var kp KeyPair
	kp.Private = priv
	copy(kp.Public[:], pub)
	return kp, nil
}

// PollTokenSize is the byte length of a poll token's raw entropy before hex
// encoding - 256 bits, the same budget KeySize's Curve25519 keys already
// carry. That margin is what docs/networking-peer-model.md's "What this
// document does not decide" section left as an open "rate limiting or abuse
// protection" question: see the poll endpoint's own doc comment
// (internal/health package) for why no separate rate limiter is layered on
// top of this margin for a first slice with zero live callers.
const PollTokenSize = 32

// GeneratePollToken mints a fresh per-allocation bearer credential for the
// controller's WireGuard peer-poll endpoint
// (docs/networking-peer-model.md's "Revocation" section), plus the SHA-256
// hash of it that the caller should checkpoint instead of the raw token -
// see PollTokenHash's rationale on lifecycle.Allocation.
// WireGuardPollTokenHash. It is generated with crypto/rand, the same entropy
// source Generate already uses for keypairs.
//
// The raw token is returned hex-encoded rather than base64: unlike
// PrivateKey.Base64 above, no external WireGuard tool has to interoperate
// with this value's encoding, so the choice is free, and hex has no
// characters (+, /, =) that ever need quoting or escaping in an HTTP
// Authorization header, a shell-quoted cloud-init script, or a log line that
// accidentally includes it - unlike base64's alphabet.
func GeneratePollToken() (string, [sha256.Size]byte, error) {
	raw := make([]byte, PollTokenSize)
	if _, err := rand.Read(raw); err != nil {
		return "", [sha256.Size]byte{}, fmt.Errorf("wireguard: generate poll token: %w", err)
	}
	return hex.EncodeToString(raw), sha256.Sum256(raw), nil
}

// HashPollToken returns the SHA-256 hash of a poll token's raw bytes, given
// the same hex encoding GeneratePollToken returns and CloudInitPayload
// embeds. The poll endpoint calls this on every request to turn a presented
// Authorization header value into the same shape as the checkpointed hash it
// compares against (in constant time) - it never compares raw token bytes,
// mirroring GeneratePollToken's own separation of "the raw secret, delivered
// once" from "the hash, checked repeatedly".
func HashPollToken(token string) ([sha256.Size]byte, error) {
	raw, err := hex.DecodeString(token)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("wireguard: decode poll token: %w", err)
	}
	return sha256.Sum256(raw), nil
}

// Peer is one entry in the peer list an allocation's cloud-init needs:
// another allocation's already-checkpointed public identity on the same
// NetworkProfile's overlay.
type Peer struct {
	AllocationID   string `json:"allocationID"`
	PublicKey      string `json:"publicKey"`
	OverlayAddress string `json:"overlayAddress"`
}

// Snapshot computes the deterministic peer list allocation `forID` needs for
// NetworkProfile `networkProfile`: every other allocation in `allocations`
// that is Phase == lifecycle.Running, shares that NetworkProfile reference,
// and has already checkpointed both a public key and an overlay address -
// ordered by allocation ID so that two calls over the same membership always
// produce byte-identical cloud-init input.
//
// `forID` is excluded from its own peer list: a WireGuard interface is never
// configured as its own peer, and including it would produce an inert or
// outright rejected self-referential entry in whatever VM-side WireGuard
// setup eventually consumes this snapshot.
//
// A candidate that has not yet checkpointed a public key or overlay address
// (still short of its own Creating->Running transition) is skipped rather
// than included with empty fields: an incomplete peer entry is worse than a
// temporarily shorter list, since the poll loop docs/networking-peer-model.md
// describes (out of scope for this change) converges every surviving
// allocation once it finishes checkpointing.
func Snapshot(forID, networkProfile string, allocations []lifecycle.Allocation) []Peer {
	if networkProfile == "" {
		return nil
	}
	peers := make([]Peer, 0, len(allocations))
	for _, a := range allocations {
		if a.ID == "" || a.ID == forID {
			continue
		}
		if a.NetworkProfile != networkProfile || a.Phase != lifecycle.Running {
			continue
		}
		if len(a.WireGuardPublicKey) == 0 || a.WireGuardOverlayAddress == "" {
			continue
		}
		peers = append(peers, Peer{
			AllocationID:   a.ID,
			PublicKey:      base64.StdEncoding.EncodeToString(a.WireGuardPublicKey),
			OverlayAddress: a.WireGuardOverlayAddress,
		})
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].AllocationID < peers[j].AllocationID })
	return peers
}

// CloudInitPayload is the minimal structured content embedded into a
// wireguard-mode allocation's cloud-init at /run/runnerscout/wireguard.json
// (internal/provider/command.go's BootstrapWithWireGuard), the same channel
// and threat model already used for the GitHub JIT token
// (internal/provider/command.go's Bootstrap). The exact wire-format
// stability, and everything about how a VM-side agent actually consumes this
// file (systemd unit, interface bring-up, poll loop), are explicitly left
// open by docs/networking-peer-model.md's "What this document does not
// decide" section - this shape exists only so that later work has something
// concrete to iterate on instead of inventing a format with no embedding
// code to validate it against.
type CloudInitPayload struct {
	PrivateKey string `json:"privateKey"` // base64, KeySize raw bytes
	// PollToken is the raw, hex-encoded bearer credential
	// (GeneratePollToken) this allocation's VM presents to the controller's
	// WireGuard peer-poll endpoint. Delivered in the clear, exactly like
	// PrivateKey and the GitHub JIT token above it in cloud-init - the same
	// accepted threat model (docs/networking-peer-model.background.md, "Why
	// trust is derived from cloud-init instead of a handshake"). The
	// controller checkpoints only this token's hash, never the raw value; see
	// lifecycle.Allocation.WireGuardPollTokenHash.
	PollToken      string `json:"pollToken"`
	OverlayAddress string `json:"overlayAddress"` // format not yet decided; see peer-model doc
	Peers          []Peer `json:"peers"`
}
