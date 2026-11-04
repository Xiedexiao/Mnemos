package types

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// ============================================================================
// 网络访问限制（KnowAxis V3.0 §16.3 "密级与安全：水印、分享、下载、导出、
// IP 限制、DLP"）。
//
// 平台此前没有任何来源限制：一个泄露的凭据在世界任何地方都能用。IP 白名单
// 是最便宜、最有效的一道收敛手段，尤其是给外部协作者与 API Key。
//
// 三个决定：
//  1. 白名单是**allow-list**，不是黑名单：黑名单永远列不全。
//  2. 支持 CIDR 与精确地址，IPv4/IPv6 都走 net.ParseCIDR 归一化——自己写
//     字符串前缀匹配会把 10.0.0.1 与 10.0.0.10 混为一谈。
//  3. 空列表 = 不限制。这是刻意的：默认拒绝会让所有存量部署升级后立刻不可用。
// ============================================================================

// NetworkAccessPolicy is one tenant's source-address restriction.
type NetworkAccessPolicy struct {
	TenantID uint64 `json:"tenant_id"`
	// AllowEntries are CIDRs or exact addresses. Empty means "no restriction".
	AllowEntries []string     `json:"allow_entries"`
	Enabled      bool         `json:"enabled"`
	parsed       []*net.IPNet `json:"-"`
	addrs        []net.IP     `json:"-"`
}

// NetworkAccessPolicyInput is the admin payload.
type NetworkAccessPolicyInput struct {
	// AllowEntries are CIDRs ("10.0.0.0/8") or exact addresses ("203.0.113.7").
	AllowEntries []string `json:"allow_entries"`
	// Enabled turns the restriction on. Entries may be stored while disabled.
	Enabled bool `json:"enabled"`
}

// MaxNetworkAllowEntries bounds one policy so a bad import cannot create an
// unreadable list.
const MaxNetworkAllowEntries = 200

// ParseNetworkAccessPolicy validates and pre-parses the entries.
//
// 解析失败直接报错而不是丢弃该条：一个写错的 CIDR 被静默丢掉，会让"我明明
// 加了公司网段"变成"所有人都进不来"，而错误信息指向具体那一行才有救。
func ParseNetworkAccessPolicy(tenantID uint64, input NetworkAccessPolicyInput) (
	*NetworkAccessPolicy, error,
) {
	entries := normalizeNetworkEntries(input.AllowEntries)
	if len(entries) > MaxNetworkAllowEntries {
		return nil, fmt.Errorf("at most %d allow entries are supported", MaxNetworkAllowEntries)
	}
	policy := &NetworkAccessPolicy{
		TenantID: tenantID, AllowEntries: entries, Enabled: input.Enabled,
	}
	for _, entry := range entries {
		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", entry)
			}
			policy.parsed = append(policy.parsed, network)
			continue
		}
		addr := net.ParseIP(entry)
		if addr == nil {
			return nil, fmt.Errorf("invalid IP address %q", entry)
		}
		policy.addrs = append(policy.addrs, addr)
	}
	return policy, nil
}

// normalizeNetworkEntries trims, dedupes and preserves order.
func normalizeNetworkEntries(entries []string) []string {
	out := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// Allows reports whether a client address may proceed.
//
// 判定顺序：策略未启用 → 放行；没有条目 → 放行（未配置即不限制）；否则必须
// 命中任一条目。无法解析的客户端地址一律拒绝——这是安全门禁，宁可让一个
// 代理配置错误的请求失败，也不能放行一个来源不明的请求。
func (p *NetworkAccessPolicy) Allows(clientIP string) bool {
	if p == nil || !p.Enabled {
		return true
	}
	if len(p.AllowEntries) == 0 {
		return true
	}
	trimmed := strings.TrimSpace(clientIP)
	// IPv4-mapped IPv6（::ffff:10.0.0.1）要按 IPv4 比较，否则一台走双栈的
	// 代理会让同一个客户端时通时不通。
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		trimmed = host
	}
	addr := net.ParseIP(strings.Trim(trimmed, "[]"))
	if addr == nil {
		return false
	}
	if v4 := addr.To4(); v4 != nil {
		addr = v4
	}
	for _, allowed := range p.addrs {
		if allowed.Equal(addr) {
			return true
		}
	}
	for _, network := range p.parsed {
		if network.Contains(addr) {
			return true
		}
	}
	return false
}

// NetworkAccessPolicyRow is the persisted form.
type NetworkAccessPolicyRow struct {
	TenantID uint64 `json:"tenant_id" gorm:"primaryKey"`
	// AllowEntries is a JSON array of CIDRs / addresses.
	AllowEntries JSON      `json:"allow_entries" gorm:"type:json;not null;default:'[]'"`
	Enabled      bool      `json:"enabled" gorm:"not null;default:false"`
	UpdatedBy    string    `json:"updated_by" gorm:"type:varchar(36);not null;default:''"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (NetworkAccessPolicyRow) TableName() string { return "network_access_policies" }

// AllowEntryList decodes the stored entries, tolerating a corrupt payload.
func (r *NetworkAccessPolicyRow) AllowEntryList() []string {
	if r == nil || len(r.AllowEntries) == 0 {
		return nil
	}
	var entries []string
	if err := json.Unmarshal(r.AllowEntries, &entries); err != nil {
		return nil
	}
	return normalizeNetworkEntries(entries)
}

// ToPolicy builds the in-memory policy from the row.
func (r *NetworkAccessPolicyRow) ToPolicy() *NetworkAccessPolicy {
	if r == nil {
		return &NetworkAccessPolicy{}
	}
	policy, err := ParseNetworkAccessPolicy(r.TenantID, NetworkAccessPolicyInput{
		AllowEntries: r.AllowEntryList(), Enabled: r.Enabled,
	})
	if err != nil {
		// 存量的坏条目按"未启用"处理：解析失败时放行（与读取失败同一取舍），
		// 而不是把整个租户锁在门外。
		return &NetworkAccessPolicy{TenantID: r.TenantID}
	}
	return policy
}
