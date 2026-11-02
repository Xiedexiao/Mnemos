package service

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// promptVersionResolver overlays a tenant's saved prompt versions onto the
// built-in prompt templates (KnowAxis V3.0 §16.5 模型与 Prompt 管理).
//
// Why this exists: `prompt_versions` was append-only and audited, but nothing
// ever read it. Saving a version changed a row and no prompt. Without this
// resolver, §16.5's "Prompt 统一版本化 / 灰度 / 可回滚" is a database table with
// no effect on answers.
//
// Resolution order, narrowest first:
//
//	tenant override (the tenant's latest version of that key)
//	platform default version (TenantID 0, if an operator published one)
//	built-in template from config/prompt_templates
//
// The built-in fallback is not resolved here: callers pass whatever the file
// configuration produced, and this resolver only replaces it when a stored
// version exists.
type promptVersionResolver struct {
	gateway interfaces.ModelGatewayService
}

// resolve returns the prompt body for templateKey, overriding fallback when a
// stored version exists.
//
// Failures are non-fatal by design: a prompt lookup must never break answering,
// so an unavailable database leaves the built-in template in place.
func (r *promptVersionResolver) resolve(
	ctx context.Context, tenantID uint64, templateKey, fallback string,
) string {
	templateKey = strings.TrimSpace(templateKey)
	if r == nil || r.gateway == nil || templateKey == "" {
		return fallback
	}
	version, err := r.gateway.LatestVersion(ctx, tenantID, templateKey)
	if err != nil || version == nil || strings.TrimSpace(version.Content) == "" {
		if err != nil {
			logger.Warnf(ctx,
				"prompt version lookup failed for %q (using built-in): %v", templateKey, err)
		}
		return fallback
	}
	// §16.5: 每次生成都要能回答"用的是哪一版"。调用方通过 PromptVersionTag 记录。
	return version.Content
}

// resolveWithTag is resolve plus the version tag an answer should record.
//
// The tag is what makes an answer reproducible: the Evidence Pack stores it, so
// "the same question gave a different answer" can be traced to a prompt change
// instead of being blamed on the model.
func (r *promptVersionResolver) resolveWithTag(
	ctx context.Context, tenantID uint64, templateKey, fallback string,
) (string, string) {
	templateKey = strings.TrimSpace(templateKey)
	if r == nil || r.gateway == nil || templateKey == "" {
		return fallback, ""
	}
	version, err := r.gateway.LatestVersion(ctx, tenantID, templateKey)
	if err != nil || version == nil || strings.TrimSpace(version.Content) == "" {
		if err != nil {
			logger.Warnf(ctx,
				"prompt version lookup failed for %q (using built-in): %v", templateKey, err)
		}
		return fallback, ""
	}
	return version.Content, promptVersionTag(templateKey, version.Version)
}

// promptVersionTag is the stable identifier recorded on answers and evidence
// packs: "<template_key>@v<version>".
func promptVersionTag(templateKey string, version int) string {
	if templateKey == "" || version <= 0 {
		return ""
	}
	return templateKey + "@v" + itoaSmall(version)
}

// itoaSmall formats a small positive integer without pulling strconv into hot
// paths that only ever format a version number.
func itoaSmall(value int) string {
	if value <= 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for value > 0 {
		pos--
		digits[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[pos:])
}

// PromptTemplateKeys returns the overridable prompt keys (§16.5). The list lives
// in types so handlers can serve it without importing this package.
func PromptTemplateKeys() []string {
	return types.PromptTemplateKeys()
}
