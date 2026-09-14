package wireguard

import (
	"errors"
	"fmt"
	"net/netip"
)

// ErrOverlayAddressesExhausted is returned by NextOverlayAddress when every
// host address in every supplied CIDR is already taken. Callers must
// surface this through their own admission-failure path (never retry
// blindly in a tight loop, never silently skip the allocation) - see
// internal/operator.HandleDesiredRunnerCount's "OverlayAddressPoolExhausted"
// condition for the one caller this codebase has today.
var ErrOverlayAddressesExhausted = errors.New("wireguard: overlay address pool exhausted")

// NextOverlayAddress deterministically picks the first host address, across
// cidrs in the order given and each cidr's own host range in ascending
// address order, that is not present in taken (a set of already-assigned
// overlay addresses, string-keyed in the same dotted/colon form
// netip.Addr.String() produces). Each cidr's network address (all host bits
// zero) and its highest address (all host bits one - the IPv4 broadcast
// address, or the topologically equivalent highest address of an IPv6
// prefix) are never returned: excluding both, uniformly for IPv4 and IPv6,
// keeps this function's notion of "usable host address" identical to the
// conventional IPv4 subnet-sizing rule every cidrs example in this
// codebase's docs already assumes, without needing an IPv4/IPv6 branch.
//
// cidrs must already be canonical, non-overlapping prefixes -
// internal/configapi/compile.go's network() already enforces exactly that
// for every NetworkMapping.CIDRs this codebase's configuration path can
// produce; operator.Config.Validate enforces the canonical-prefix half of
// the same check for the mounted-config path, which bypasses compile.go
// entirely. This function does not re-validate overlap between cidrs
// itself - a caller that violates that precondition may hand out one
// address to two allocations, which is exactly the invariant those two
// validators exist to prevent upstream instead of here.
//
// This is a full linear scan of each cidr's host range, not an interval
// tree or bitmap. That is deliberate: this codebase does not need a
// general-purpose IPAM (see docs/networking-peer-model.md's overlay address
// allocation section), and every documented/example wireguard cidrs value
// is a /24 or smaller (at most 254 usable addresses) - a linear scan over
// that many candidates, run once per newly admitted allocation (bounded by
// Config.MaxRunners, at most 10), is not a performance concern. A caller
// that configures a much larger cidr accepts a correspondingly slower scan
// in the (rare, since exhaustion requires that many concurrently active
// allocations) worst case where the pool is actually full.
func NextOverlayAddress(cidrs []string, taken map[string]bool) (string, error) {
	for _, c := range cidrs {
		prefix, err := netip.ParsePrefix(c)
		if err != nil {
			return "", fmt.Errorf("wireguard: invalid overlay CIDR %q: %w", c, err)
		}
		prefix = prefix.Masked()
		network := prefix.Addr()
		last := lastAddr(prefix)
		for addr := network.Next(); addr.IsValid() && addr != last && prefix.Contains(addr); addr = addr.Next() {
			s := addr.String()
			if !taken[s] {
				return s, nil
			}
		}
	}
	return "", ErrOverlayAddressesExhausted
}

// lastAddr returns the highest address in prefix (all host bits set to
// one) - prefix's IPv4 broadcast address, or the equivalent highest address
// of an IPv6 prefix. prefix must already be masked (prefix == prefix.Masked()).
func lastAddr(prefix netip.Prefix) netip.Addr {
	base := prefix.Addr()
	bytes := base.AsSlice()
	hostBits := base.BitLen() - prefix.Bits()
	for i := len(bytes) - 1; hostBits > 0 && i >= 0; i-- {
		if hostBits >= 8 {
			bytes[i] = 0xff
			hostBits -= 8
			continue
		}
		bytes[i] |= byte(0xff) >> (8 - hostBits)
		hostBits = 0
	}
	addr, ok := netip.AddrFromSlice(bytes)
	if !ok {
		return base
	}
	if base.Is4In6() {
		addr = addr.Unmap()
	}
	return addr
}
