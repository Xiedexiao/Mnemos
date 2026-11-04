package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkPolicyAllowsCIDR covers the allow-list semantics.
func TestNetworkPolicyAllowsCIDR(t *testing.T) {
	policy, err := ParseNetworkAccessPolicy(7, NetworkAccessPolicyInput{
		AllowEntries: []string{"10.0.0.0/8", "203.0.113.7", "2001:db8::/32"},
		Enabled:      true,
	})
	require.NoError(t, err)

	assert.True(t, policy.Allows("10.1.2.3"), "inside the CIDR")
	assert.True(t, policy.Allows("203.0.113.7"), "an exact address")
	assert.True(t, policy.Allows("2001:db8::1"), "IPv6 CIDR")
	assert.False(t, policy.Allows("192.168.1.1"), "outside every entry")
	assert.False(t, policy.Allows("203.0.113.8"), "an exact entry is not a prefix")
}

// TestNetworkPolicyHandlesPortsAndMappedV4 covers proxy-style addresses.
func TestNetworkPolicyHandlesPortsAndMappedV4(t *testing.T) {
	policy, err := ParseNetworkAccessPolicy(7, NetworkAccessPolicyInput{
		AllowEntries: []string{"10.0.0.0/8"}, Enabled: true,
	})
	require.NoError(t, err)
	assert.True(t, policy.Allows("10.1.2.3:54321"), "a host:port must be split")
	assert.True(t, policy.Allows("::ffff:10.1.2.3"),
		"an IPv4-mapped v6 address must compare as v4, or a dual-stack proxy flaps")
	assert.False(t, policy.Allows("[2001:db8::1]:443"))
}

// TestNetworkPolicyDisabledOrEmptyAllowsEverything keeps upgrades from locking
// out an existing deployment.
func TestNetworkPolicyDisabledOrEmptyAllowsEverything(t *testing.T) {
	disabled, err := ParseNetworkAccessPolicy(7, NetworkAccessPolicyInput{
		AllowEntries: []string{"10.0.0.0/8"}, Enabled: false,
	})
	require.NoError(t, err)
	assert.True(t, disabled.Allows("192.168.1.1"), "a disabled policy never blocks")

	empty, err := ParseNetworkAccessPolicy(7, NetworkAccessPolicyInput{Enabled: true})
	require.NoError(t, err)
	assert.True(t, empty.Allows("192.168.1.1"),
		"enabled with no entries means no restriction, not \"block everyone\"")

	var nilPolicy *NetworkAccessPolicy
	assert.True(t, nilPolicy.Allows("192.168.1.1"))
}

// TestNetworkPolicyRejectsBadEntries is the fail-loudly rule: a typo must be an
// error, not a silently dropped entry.
func TestNetworkPolicyRejectsBadEntries(t *testing.T) {
	_, err := ParseNetworkAccessPolicy(7, NetworkAccessPolicyInput{
		AllowEntries: []string{"10.0.0.0/333"}, Enabled: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.0.0.0/333")

	_, err = ParseNetworkAccessPolicy(7, NetworkAccessPolicyInput{
		AllowEntries: []string{"not-an-ip"}, Enabled: true,
	})
	require.Error(t, err)
}

// TestNetworkPolicyBoundEntries caps the list.
func TestNetworkPolicyBoundEntries(t *testing.T) {
	// The list is deduped before the cap is applied, so the entries must be
	// genuinely distinct for this test to mean anything.
	entries := make([]string, MaxNetworkAllowEntries+1)
	for i := range entries {
		entries[i] = "10." + itoaTest(i/256) + "." + itoaTest(i%256) + ".1/32"
	}
	_, err := ParseNetworkAccessPolicy(7, NetworkAccessPolicyInput{AllowEntries: entries})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most")
}

// TestNetworkPolicyUnknownAddressDenied keeps a malformed client address from
// being treated as "no opinion".
func TestNetworkPolicyUnknownAddressDenied(t *testing.T) {
	policy, err := ParseNetworkAccessPolicy(7, NetworkAccessPolicyInput{
		AllowEntries: []string{"10.0.0.0/8"}, Enabled: true,
	})
	require.NoError(t, err)
	assert.False(t, policy.Allows(""))
	assert.False(t, policy.Allows("garbage"))
}

// itoaTest renders a small non-negative int without importing strconv.
func itoaTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
