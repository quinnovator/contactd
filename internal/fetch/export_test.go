package fetch

import "net/netip"

// Public exposes the dial-time address rule to its table test.
func Public(ip netip.Addr) bool { return public(ip) }
