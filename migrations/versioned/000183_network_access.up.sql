DO $$ BEGIN RAISE NOTICE '[Migration 000183] Adding network access policies...'; END $$;

-- 来源地址白名单（KnowAxis V3.0 §16.3 "密级与安全：... IP 限制 ..."）。
--
-- 平台此前没有任何来源限制：一个泄露的凭据在世界任何地方都能用。白名单是
-- 最便宜、最有效的一道收敛手段，尤其是给外部协作者与 API Key。
--
-- 独立表而不是塞进 tenant KV：它在每个请求上被读取，KV blob 意味着请求路径
-- 上要做 JSON 解析与校验。
--
-- 空列表 = 不限制。默认拒绝会让所有存量部署升级后立刻不可用。

CREATE TABLE IF NOT EXISTS network_access_policies (
    tenant_id INTEGER PRIMARY KEY,
    -- CIDR 或精确地址的 JSON 数组；IPv4/IPv6 都支持
    allow_entries JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    updated_by VARCHAR(36) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
