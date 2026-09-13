// Package tunnel brings up a real userspace WireGuard interface - the
// actual data-plane device, not merely the identity/snapshot values
// internal/wireguard computes - using golang.zx2c4.com/wireguard/device on
// the netstack (gVisor) TUN backend
// (golang.zx2c4.com/wireguard/tun/netstack), per
// docs/networking-control-plane.md's recommendation and
// docs/networking-peer-model.md's peer model. It is the reusable Go library
// core a future VM-side agent (a systemd unit or a small binary embedding
// this package) would call into to actually establish connectivity; this
// repository does not build that caller yet, and this package does not
// decide anything about it.
//
// This is a separate subpackage of internal/wireguard, not more files in
// that package, because golang.zx2c4.com/wireguard/tun/netstack pulls in
// gVisor's own network stack - over 150 transitive packages
// (`go list -deps golang.zx2c4.com/wireguard/tun/netstack`) - that nothing
// in this repository's production binary needs today. internal/wireguard
// itself is already imported by internal/health and internal/provider,
// both compiled into cmd/runnerscout; keeping the actual device/netstack
// dependency isolated to this subpackage means cmd/runnerscout's own
// dependency graph and binary size are unaffected until something in this
// repository actually calls BringUp, which today only this package's own
// tests do.
package tunnel

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	wgdevice "golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/tsouza/runnerscout/internal/wireguard"
)

// DefaultMTU is WireGuard's own conventional interface MTU (1420: room for
// the 60-byte WireGuard/UDP/IP overhead under a standard 1500-byte Ethernet
// MTU on the underlying network), used when Config.MTU is left zero.
const DefaultMTU = 1420

// Peer is one entry in the peer list a Tunnel is configured with: another
// allocation's public key and overlay address - the same two identity
// fields wireguard.Peer already carries in CloudInitPayload - plus an
// Endpoint.
//
// Endpoint is deliberately not part of wireguard.Peer/CloudInitPayload: the
// overlay address there is the peer's INNER address (routed inside the
// WireGuard tunnel, via AllowedIPs), never the OUTER address the real UDP
// transport needs to reach that peer's device.Device before any tunnel
// traffic can flow. Nothing in docs/networking-peer-model.md decides how a
// VM-side agent learns a peer's real outer network address (its own "What
// this document does not decide" section leaves the poll endpoint's wire
// schema, and everything past it, open) - that remains a real gap for
// whatever VM-side integration eventually calls BringUp, not something
// this package resolves. Endpoint may be left as the zero netip.AddrPort
// when this side never needs to initiate to that peer: a still-unreachable
// peer can become reachable once it sends this side a valid handshake
// first, the same "roaming" behavior real WireGuard already relies on for
// clients behind NAT.
type Peer struct {
	PublicKey      wireguard.PublicKey
	OverlayAddress netip.Addr
	Endpoint       netip.AddrPort
}

// Config is the minimal input BringUp needs: a private key, this tunnel's
// own overlay address, the outer UDP port to listen on, and its initial
// peer list - exactly the fields docs/networking-peer-model.md's
// CloudInitPayload already carries (plus Endpoint; see Peer's doc comment)
// and nothing else. This is deliberately not a general-purpose WireGuard
// interface configuration surface (no PSK, no multi-address, no route
// table beyond a peer's own single overlay address, no DNS) - those are
// either out of this task's scope (EnrollmentRef/PSK) or not needed by
// anything that calls BringUp yet.
type Config struct {
	PrivateKey     wireguard.PrivateKey
	OverlayAddress netip.Addr
	// ListenPort is the outer UDP port this tunnel's device binds to. Zero
	// lets the OS choose an ephemeral port, which is fine for a side that
	// only ever needs to initiate outbound (every Peer.Endpoint set), but
	// unusable for a side any peer needs to dial by a fixed address.
	ListenPort uint16
	Peers      []Peer
	// MTU is the TUN interface's MTU. Zero uses DefaultMTU.
	MTU int
}

// Tunnel is a live userspace WireGuard interface: a real device.Device
// bound to a real outer UDP socket, backed by a real netstack (gVisor) TUN
// device for the inner overlay address space. Close releases both.
type Tunnel struct {
	dev  *wgdevice.Device
	tnet *netstack.Net
}

// BringUp brings up a real WireGuard interface from cfg: it validates cfg,
// creates the netstack TUN device for cfg.OverlayAddress, constructs a
// device.Device bound to cfg.ListenPort via the real UDP conn.Bind
// (conn.NewDefaultBind - the same bind golang.zx2c4.com/wireguard's own
// http_client.go/http_server.go examples under tun/netstack/examples use),
// configures cfg.PrivateKey and cfg.Peers through the real UAPI
// configuration protocol (device.Device.IpcSet -
// https://www.wireguard.com/xplatform/#configuration-protocol), and brings
// the device up. Any error leaves nothing running: a failure partway
// through closes whatever was already created before returning.
func BringUp(cfg Config) (*Tunnel, error) {
	if !cfg.OverlayAddress.IsValid() {
		return nil, fmt.Errorf("wireguard/tunnel: overlay address is required")
	}
	seen := map[wireguard.PublicKey]bool{}
	for _, p := range cfg.Peers {
		if !p.OverlayAddress.IsValid() {
			return nil, fmt.Errorf("wireguard/tunnel: peer with public key %s has no overlay address", p.PublicKey)
		}
		if seen[p.PublicKey] {
			return nil, fmt.Errorf("wireguard/tunnel: duplicate peer public key %s", p.PublicKey)
		}
		seen[p.PublicKey] = true
	}

	mtu := cfg.MTU
	if mtu == 0 {
		mtu = DefaultMTU
	}

	tunDevice, tnet, err := netstack.CreateNetTUN([]netip.Addr{cfg.OverlayAddress}, nil, mtu)
	if err != nil {
		return nil, fmt.Errorf("wireguard/tunnel: create netstack TUN: %w", err)
	}

	dev := wgdevice.NewDevice(tunDevice, conn.NewDefaultBind(), wgdevice.NewLogger(wgdevice.LogLevelSilent, ""))

	if err := dev.IpcSet(uapiConfig(cfg)); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard/tunnel: configure device: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard/tunnel: bring device up: %w", err)
	}

	return &Tunnel{dev: dev, tnet: tnet}, nil
}

// uapiConfig renders cfg into the WireGuard cross-platform UAPI
// configuration protocol's text format, the same format
// golang.zx2c4.com/wireguard's own tun/netstack/examples construct by hand
// and IpcSet parses (device/uapi.go's handleDeviceLine/handlePeerLine).
// Keys and endpoint addresses are hex/textual per that protocol, not the
// base64 encoding wireguard.PrivateKey.Base64/PublicKey.String use
// elsewhere in this repository for cloud-init and checkpoints - those two
// encodings serve different consumers and are not interchangeable.
func uapiConfig(cfg Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", hex.EncodeToString(cfg.PrivateKey[:]))
	if cfg.ListenPort != 0 {
		fmt.Fprintf(&b, "listen_port=%d\n", cfg.ListenPort)
	}
	for _, p := range cfg.Peers {
		writePeerBlock(&b, p)
	}
	return b.String()
}

// writePeerBlock appends one peer's UAPI configuration-protocol lines to b:
// its public key, an optional endpoint, and its single-host AllowedIPs
// entry. Shared by uapiConfig (the full initial-peer-list block BringUp
// sends) and AddPeer (a single-peer block sent on its own) so the two never
// drift on how a peer is rendered.
func writePeerBlock(b *strings.Builder, p Peer) {
	fmt.Fprintf(b, "public_key=%s\n", hex.EncodeToString(p.PublicKey[:]))
	if p.Endpoint.IsValid() {
		fmt.Fprintf(b, "endpoint=%s\n", p.Endpoint.String())
	}
	fmt.Fprintf(b, "allowed_ip=%s\n", singleHostPrefix(p.OverlayAddress))
}

// singleHostPrefix returns addr as a /32 (IPv4) or /128 (IPv6) prefix - a
// peer's AllowedIPs entry authorizing exactly its own single overlay
// address, never a wider subnet. WireGuard's own AllowedIPs mechanism is
// what makes peer revocation an actual data-plane effect rather than only
// a config-file change: removing a peer removes its AllowedIPs entries
// too, so a subsequent packet claiming that source has nothing to route it
// through or decrypt it against.
func singleHostPrefix(addr netip.Addr) netip.Prefix {
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	return netip.PrefixFrom(addr, bits)
}

// Net returns the netstack-backed *netstack.Net for this tunnel: the
// net.Dialer/net.Listener-compatible surface
// (DialUDP/ListenUDP/DialTCP/ListenTCP/DialContext/...) for actually
// sending or receiving application traffic through the tunnel's inner
// overlay address space. See golang.zx2c4.com/wireguard/tun/netstack's own
// godoc for the full method set; this package does not wrap or narrow it
// further; it is already exactly the surface a caller dialing/accepting
// through the tunnel needs.
func (t *Tunnel) Net() *netstack.Net {
	return t.tnet
}

// AddPeer configures a single new peer on this tunnel's already-running
// device, without disturbing any peer already configured (by BringUp's
// initial Peers list or a previous AddPeer call): it sends device.Device.
// IpcSet exactly one peer's UAPI configuration block (writePeerBlock, the
// same rendering uapiConfig uses for BringUp's initial list), with no
// "private_key"/"listen_port" device-level line and no "replace_peers=true"
// line. Per the UAPI configuration protocol IpcSet implements
// (device/uapi.go's IpcSetOperation/handleDeviceLine -
// https://www.wireguard.com/xplatform/#configuration-protocol),
// "replace_peers=true" is the only thing that ever clears the existing peer
// set, and a peer is looked up/created by its own public_key line
// independently of any other peer already configured - so this call can
// only add (or, for an already-configured public key, update) the one peer
// it describes.
//
// This is the data-plane action a VM-side agent's poll loop uses when it
// observes a new peer added to its authoritative list (see RemovePeer below
// for the removal counterpart docs/networking-peer-model.md's "Revocation"
// section already covers).
func (t *Tunnel) AddPeer(p Peer) error {
	if !p.OverlayAddress.IsValid() {
		return fmt.Errorf("wireguard/tunnel: peer with public key %s has no overlay address", p.PublicKey)
	}
	var b strings.Builder
	writePeerBlock(&b, p)
	if err := t.dev.IpcSet(b.String()); err != nil {
		return fmt.Errorf("wireguard/tunnel: add peer %s: %w", p.PublicKey, err)
	}
	return nil
}

// RemovePeer removes a peer from this tunnel's device entirely: its
// session keys, routing (AllowedIPs) entries and handshake state are all
// torn down (device.Device.RemovePeer), not merely marked stale. This is
// the data-plane action a controller-side revocation
// (docs/networking-peer-model.md's "Revocation" section) ultimately drives
// once a VM-side agent's poll loop observes a peer's removal from its
// authoritative list: after this call, any packet arriving claiming to be
// from pub is silently dropped, never delivered.
func (t *Tunnel) RemovePeer(pub wireguard.PublicKey) error {
	t.dev.RemovePeer(wgdevice.NoisePublicKey(pub))
	return nil
}

// Close tears down the device (which also closes the underlying netstack
// TUN device - device.Device.Close's own documented behavior) and releases
// the outer UDP socket. Close is idempotent: device.Device.Close already
// no-ops on an already-closed device, and this method adds no additional
// state that could make a second call unsafe.
func (t *Tunnel) Close() error {
	t.dev.Close()
	return nil
}
