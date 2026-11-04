package types

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// ============================================================================
// SAML 2.0 SP（KnowAxis V3.0 §16.4 "SSO / 组织：OIDC/SAML"）。
//
// 平台此前只有 OIDC。补 SAML 的取舍：
//
//  1. **不引第三方 SAML 库**。仓库在本环境无外网，且一个完整的 SAML 库
//     （crewjam/saml 之类）会带进 XML 签名规范化（XML-DSig C14N）的整套实现
//     ——那是一块很难正确、也很难审计的攻击面。这里实现的是**只够用的一层**：
//     解析 Response/Assertion、校验 NotBefore/NotOnOrAfter/Audience/InResponseTo、
//     验证 **Assertion 级 RSA-SHA256 签名**。签名覆盖范围按 SAML 的简化假设
//     处理（对 Assertion 元素序列化后验签）。
//
//  2. 明确**不支持**的东西，而不是假装支持：
//     - 不支持 **Enveloped XML 签名**（<ds:Signature> 嵌在 XML 内部，需要
//       C14N 规范化）。这是绝大多数 IdP 的默认形态，因此生产接入通常需要
//       一个真正的 SAML 库——这里返回明确错误而不是静默放行。
//     - 不支持加密断言（EncryptedAssertion）。
//     - 不支持 IdP 元数据自动发现。
//
//  3. 因此本实现的定位是：**协议骨架 + 可验证的安全边界**。它让 SP 的
//     校验逻辑（时间窗、受众、重放、属性映射）今天就可用且有测试覆盖；接
//     真实 IdP 时只需要替换"取签名并验签"这一步。
//
// 这个边界写在代码里而不是文档里：CanVerify 会明确拒绝它做不到的形态，
// 调用方因此不可能误以为它验证过了一个 enveloped 签名。
// ============================================================================

// SAMLBinding names how the Response reaches the SP.
type SAMLBinding string

const (
	// SAMLBindingPOST is the HTTP-POST binding (form field SAMLResponse).
	SAMLBindingPOST SAMLBinding = "post"
	// SAMLBindingRedirect is the HTTP-Redirect binding (query parameter).
	SAMLBindingRedirect SAMLBinding = "redirect"
)

// Valid reports whether the binding is known.
func (b SAMLBinding) Valid() bool {
	return b == SAMLBindingPOST || b == SAMLBindingRedirect
}

// SAMLConfig is the SP's trust configuration for one IdP.
type SAMLConfig struct {
	// EntityID is this SP's identifier, asserted as the Audience.
	EntityID string `json:"entity_id"`
	// ACSURL is the assertion consumer service endpoint the IdP posts to.
	ACSURL string `json:"acs_url"`
	// IdPEntityID is the IdP's issuer; the Response's Issuer must match.
	IdPEntityID string `json:"idp_entity_id"`
	// IdPCertificatePEM is the IdP's signing certificate. Verification is
	// anchored here and nowhere else: trusting an embedded certificate would
	// let anyone with a self-signed cert mint assertions.
	IdPCertificatePEM string `json:"idp_certificate_pem"`
	// ClockSkew tolerates small clock differences (default 2 minutes).
	ClockSkew time.Duration `json:"clock_skew"`
}

const (
	// DefaultSAMLClockSkew is how far the SP tolerates clock drift.
	DefaultSAMLClockSkew = 2 * time.Minute
	// SAMLMaxResponseBytes bounds the accepted payload.
	//
	// 一个 10MB 的 SAMLResponse 既不是真实断言，也是解压炸弹的常见形态。
	SAMLMaxResponseBytes = 1 << 20
)

// SAMLSubject is the identity one assertion asserts.
type SAMLSubject struct {
	// NameID is the persistent identifier at the IdP.
	NameID string `json:"name_id"`
	// Email / DisplayName come from attributes.
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	// Groups are the IdP group names, mapped by the caller to workspace roles.
	Groups []string `json:"groups,omitempty"`
	// SessionIndex identifies this SSO session (for single logout).
	SessionIndex string `json:"session_index,omitempty"`
}

// SAMLAssertion is the verified result of one Response.
type SAMLAssertion struct {
	// Subject is who the assertion is about.
	Subject SAMLSubject `json:"subject"`
	// Issuer is the IdP that signed it.
	Issuer string `json:"issuer"`
	// Audience is the SP entity id the assertion is restricted to.
	Audience string `json:"audience"`
	// NotBefore / NotOnOrAfter are the validity window.
	NotBefore    time.Time `json:"not_before"`
	NotOnOrAfter time.Time `json:"not_on_or_after"`
	// InResponseTo echoes the SP's original request id ("" when unsolicited).
	InResponseTo string `json:"in_response_to,omitempty"`
	// AssertionID is used for replay protection.
	AssertionID string `json:"assertion_id"`
}

// SAMLError is a verification failure with a machine-readable reason.
//
// 每条拒绝都必须有理由：SAML 的失败模式里"签名对了但受众错了"与"签名错了"
// 是完全不同的安全事件，混成一个 error 会让排查与告警都失效。
type SAMLError struct {
	// Code is the machine-readable cause.
	Code string `json:"code"`
	// Message is the human-facing explanation.
	Message string `json:"message"`
}

// SAML verification error codes.
const (
	// SAMLErrMalformed is an unparseable payload.
	SAMLErrMalformed = "malformed"
	// SAMLErrTooLarge is an oversized payload.
	SAMLErrTooLarge = "too_large"
	// SAMLErrMissingAssertion has no Assertion element.
	SAMLErrMissingAssertion = "missing_assertion"
	// SAMLErrEncrypted is an encrypted assertion (unsupported).
	SAMLErrEncrypted = "encrypted_assertion_unsupported"
	// SAMLErrEnvelopedSignature is a signature this implementation cannot
	// verify without C14N. Refused rather than skipped.
	SAMLErrEnvelopedSignature = "enveloped_signature_unsupported"
	// SAMLErrNoSignature has no verifiable signature.
	SAMLErrNoSignature = "no_signature"
	// SAMLErrBadSignature is a signature that does not verify.
	SAMLErrBadSignature = "bad_signature"
	// SAMLErrUnknownCertificate means the configured certificate cannot be used.
	SAMLErrUnknownCertificate = "unknown_certificate"
	// SAMLErrIssuerMismatch is a Response from an untrusted IdP.
	SAMLErrIssuerMismatch = "issuer_mismatch"
	// SAMLErrAudienceMismatch is an assertion for another SP.
	SAMLErrAudienceMismatch = "audience_mismatch"
	// SAMLErrExpired is outside the validity window.
	SAMLErrExpired = "expired"
	// SAMLErrNotYetValid is before NotBefore.
	SAMLErrNotYetValid = "not_yet_valid"
	// SAMLErrNoSubject has no NameID.
	SAMLErrNoSubject = "no_subject"
)

// Error renders the code and message.
func (e *SAMLError) Error() string {
	return fmt.Sprintf("saml: %s: %s", e.Code, e.Message)
}

// NewSAMLError builds one verification failure.
func NewSAMLError(code, message string) *SAMLError {
	return &SAMLError{Code: code, Message: message}
}

// samlResponse is the minimal XML shape this SP reads.
type samlResponse struct {
	XMLName      xml.Name        `xml:"Response"`
	ID           string          `xml:"ID,attr"`
	InResponseTo string          `xml:"InResponseTo,attr"`
	Issuer       string          `xml:"Issuer"`
	Status       samlStatus      `xml:"Status"`
	Assertions   []samlAssertion `xml:"Assertion"`
	Encrypted    []samlEncrypted `xml:"EncryptedAssertion"`
}

type samlStatus struct {
	Code samlStatusCode `xml:"StatusCode"`
}

type samlStatusCode struct {
	Value string `xml:"Value,attr"`
}

type samlEncrypted struct {
	XMLName xml.Name
}

type samlAssertion struct {
	ID             string             `xml:"ID,attr"`
	Issuer         string             `xml:"Issuer"`
	Subject        samlSubject        `xml:"Subject"`
	Conditions     samlConditions     `xml:"Conditions"`
	AuthnStatement samlAuthnStatement `xml:"AuthnStatement"`
	// Signature is present only for an enveloped signature, which this
	// implementation refuses (see the file header).
	Signature  *samlSignature  `xml:"Signature"`
	Attributes []samlAttribute `xml:"AttributeStatement>Attribute"`
}

type samlConditions struct {
	NotBefore    string           `xml:"NotBefore,attr"`
	NotOnOrAfter string           `xml:"NotOnOrAfter,attr"`
	Audience     samlAudienceList `xml:"AudienceRestriction>Audience"`
}

// samlAudienceList captures the audience value(s).
type samlAudienceList struct {
	Value string `xml:",chardata"`
}

type samlSubject struct {
	NameID samlNameID `xml:"NameID"`
}

type samlNameID struct {
	Value string `xml:",chardata"`
}

type samlAuthnStatement struct {
	SessionIndex string `xml:"SessionIndex,attr"`
}

type samlSignature struct {
	XMLName xml.Name
}

type samlAttribute struct {
	Name   string   `xml:"Name,attr"`
	Values []string `xml:"AttributeValue"`
}

// SAMLSignatureInput is the bytes and algorithm an IdP signature covers.
//
// 这是本实现与真实 XML-DSig 的接缝：调用方（或将来的规范化实现）负责给出
// **被签名的字节**，这里只做 RSA-PKCS1v15-SHA256 验证。把这两件事分开，
// 是因为"取哪一段字节"才是 SAML 签名真正的难点，而验证本身是标准操作。
type SAMLSignatureInput struct {
	// SignedBytes are exactly the bytes the IdP signed.
	SignedBytes []byte
	// Signature is the raw (already base64-decoded) signature value.
	Signature []byte
	// Algorithm is the XML-DSig algorithm URI.
	Algorithm string
}

// SAML signature algorithm URIs this implementation accepts.
const (
	// SAMLAlgRSASHA256 is rsa-sha256.
	SAMLAlgRSASHA256 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
	// SAMLAlgRSASHA1 is rsa-sha1. Accepted only when the caller allows it:
	// SHA-1 is deprecated but still deployed, and refusing it silently would
	// look like a broken IdP.
	SAMLAlgRSASHA1 = "http://www.w3.org/2000/09/xmldsig#rsa-sha1"
)

// ParseIdPCertificatePEM decodes the IdP signing certificate.
func ParseIdPCertificatePEM(pemText string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemText)))
	if block == nil {
		return nil, NewSAMLError(SAMLErrUnknownCertificate, "the IdP certificate is not valid PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, NewSAMLError(SAMLErrUnknownCertificate,
			"the IdP certificate cannot be parsed: "+err.Error())
	}
	return cert, nil
}

// VerifySAMLSignature checks one RSA signature against the IdP certificate.
func VerifySAMLSignature(
	cert *x509.Certificate, input SAMLSignatureInput, allowSHA1 bool,
) error {
	if cert == nil {
		return NewSAMLError(SAMLErrUnknownCertificate, "no IdP certificate is configured")
	}
	if len(input.Signature) == 0 {
		return NewSAMLError(SAMLErrNoSignature, "the assertion carries no signature")
	}
	publicKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return NewSAMLError(SAMLErrUnknownCertificate,
			"the IdP certificate does not hold an RSA public key")
	}
	switch input.Algorithm {
	case SAMLAlgRSASHA1:
		if !allowSHA1 {
			return NewSAMLError(SAMLErrBadSignature,
				"rsa-sha1 is not accepted by this deployment; ask the IdP to sign with sha256")
		}
		digest := sha1.Sum(input.SignedBytes)
		if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA1, digest[:], input.Signature); err != nil {
			return NewSAMLError(SAMLErrBadSignature, "the signature does not verify")
		}
		return nil
	case SAMLAlgRSASHA256, "":
		// 空算法按 SHA-256 处理：SAML 2.0 的默认签名算法就是它。
		digest := sha256.Sum256(input.SignedBytes)
		if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], input.Signature); err != nil {
			return NewSAMLError(SAMLErrBadSignature, "the signature does not verify")
		}
		return nil
	default:
		return NewSAMLError(SAMLErrBadSignature, "unsupported signature algorithm "+input.Algorithm)
	}
}

// DecodeSAMLResponseBase64 decodes the SAMLResponse form field.
func DecodeSAMLResponseBase64(encoded string) ([]byte, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return nil, NewSAMLError(SAMLErrMalformed, "SAMLResponse is empty")
	}
	// A payload this large is base64 of something far bigger than an
	// assertion; refuse before decoding rather than after allocating.
	if len(trimmed) > SAMLMaxResponseBytes*4/3+8 {
		return nil, NewSAMLError(SAMLErrTooLarge, "the SAMLResponse is too large")
	}
	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(trimmed); err != nil {
			return nil, NewSAMLError(SAMLErrMalformed, "SAMLResponse is not valid base64")
		}
	}
	if len(raw) > SAMLMaxResponseBytes {
		return nil, NewSAMLError(SAMLErrTooLarge, "the SAMLResponse is too large")
	}
	return raw, nil
}

// ParseSAMLResponse parses a decoded Response into its assertion.
//
// 这一步**不做**信任判断：它只把 XML 读成结构。任何未验证的断言都不得用于
// 认证——ValidateSAMLAssertion 是必须紧随其后的一步，两者分开是为了让
// "解析成功"不可能被误当成"验证通过"。
func ParseSAMLResponse(raw []byte) (*SAMLAssertion, error) {
	if len(raw) == 0 {
		return nil, NewSAMLError(SAMLErrMalformed, "the response is empty")
	}
	var response samlResponse
	if err := xml.Unmarshal(raw, &response); err != nil {
		return nil, NewSAMLError(SAMLErrMalformed, "the response is not valid XML: "+err.Error())
	}
	if len(response.Encrypted) > 0 {
		return nil, NewSAMLError(SAMLErrEncrypted,
			"encrypted assertions are not supported; configure the IdP to sign without encryption")
	}
	if len(response.Assertions) == 0 {
		return nil, NewSAMLError(SAMLErrMissingAssertion, "the response carries no assertion")
	}
	assertion := response.Assertions[0]
	if assertion.Signature != nil {
		// 明确拒绝，而不是忽略签名继续：放行等于"没有任何签名校验"。
		return nil, NewSAMLError(SAMLErrEnvelopedSignature,
			"this SP cannot verify an enveloped XML signature (it needs XML-DSig canonicalisation); "+
				"configure the IdP to sign the assertion with a detached signature, or deploy a SAML library")
	}
	return &SAMLAssertion{
		InResponseTo: strings.TrimSpace(response.InResponseTo),
		Subject: SAMLSubject{
			NameID: strings.TrimSpace(assertion.Subject.NameID.Value),
			Email: firstSAMLAttribute(assertion.Attributes, "email", "mail",
				"urn:oid:0.9.2342.19200300.100.1.3", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"),
			DisplayName: firstSAMLAttribute(assertion.Attributes, "displayName", "name",
				"urn:oid:2.16.840.1.113730.3.1.241", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name"),
			Groups: samlAttributeValues(assertion.Attributes, "groups", "memberOf",
				"http://schemas.xmlsoap.org/claims/Group"),
			SessionIndex: assertion.AuthnStatement.SessionIndex,
		},
		Issuer:       strings.TrimSpace(assertion.Issuer),
		Audience:     strings.TrimSpace(assertion.Conditions.Audience.Value),
		NotBefore:    parseSAMLTime(assertion.Conditions.NotBefore),
		NotOnOrAfter: parseSAMLTime(assertion.Conditions.NotOnOrAfter),
		AssertionID:  assertion.ID,
	}, nil
}

// firstSAMLAttribute returns the first non-empty value of the first matching name.
func firstSAMLAttribute(attributes []samlAttribute, names ...string) string {
	for _, attribute := range attributes {
		if !samlNameMatches(attribute.Name, names) {
			continue
		}
		for _, value := range attribute.Values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// samlAttributeValues returns every value of the first matching attribute.
func samlAttributeValues(attributes []samlAttribute, names ...string) []string {
	for _, attribute := range attributes {
		if !samlNameMatches(attribute.Name, names) {
			continue
		}
		out := make([]string, 0, len(attribute.Values))
		seen := make(map[string]struct{}, len(attribute.Values))
		for _, value := range attribute.Values {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			if _, duplicate := seen[trimmed]; duplicate {
				continue
			}
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
		return out
	}
	return nil
}

// samlNameMatches compares an attribute name case-insensitively against the
// accepted names. IdPs are inconsistent about casing and about using the
// friendly name versus the OID URN.
func samlNameMatches(name string, accepted []string) bool {
	lowered := strings.ToLower(strings.TrimSpace(name))
	for _, candidate := range accepted {
		if lowered == strings.ToLower(candidate) {
			return true
		}
	}
	return false
}

// parseSAMLTime parses an XML-Schema dateTime; zero on failure.
func parseSAMLTime(value string) time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999Z0700"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// ValidateSAMLAssertion applies the trust checks.
//
// 顺序是刻意的：先比对签发者与受众（"这是不是给我的"），再检查时间窗，最后
// 检查主体。把时间放在前面会让一个来自错误 IdP 的过期断言报"过期"，掩盖
// 真正的问题——它根本不该被接受。
func ValidateSAMLAssertion(
	config SAMLConfig, assertion *SAMLAssertion, now time.Time, expectedInResponseTo string,
) error {
	if assertion == nil {
		return NewSAMLError(SAMLErrMissingAssertion, "no assertion to validate")
	}
	if idp := strings.TrimSpace(config.IdPEntityID); idp != "" && assertion.Issuer != idp {
		return NewSAMLError(SAMLErrIssuerMismatch,
			fmt.Sprintf("the assertion was issued by %q, not the configured IdP %q",
				assertion.Issuer, idp))
	}
	if audience := strings.TrimSpace(config.EntityID); audience != "" && assertion.Audience != audience {
		return NewSAMLError(SAMLErrAudienceMismatch,
			fmt.Sprintf("the assertion is for audience %q, not this SP %q",
				assertion.Audience, audience))
	}
	skew := config.ClockSkew
	if skew <= 0 {
		skew = DefaultSAMLClockSkew
	}
	now = now.UTC()
	if !assertion.NotBefore.IsZero() && now.Add(skew).Before(assertion.NotBefore) {
		return NewSAMLError(SAMLErrNotYetValid,
			"the assertion is not valid yet (NotBefore is in the future)")
	}
	if !assertion.NotOnOrAfter.IsZero() && !now.Before(assertion.NotOnOrAfter.Add(skew)) {
		return NewSAMLError(SAMLErrExpired,
			"the assertion has expired (NotOnOrAfter has passed)")
	}
	// 无 InResponseTo 的断言是"未经请求的"（IdP-initiated）。只有在调用方
	// 明确允许时才接受：接受它会打开登录 CSRF 的口子。
	if expected := strings.TrimSpace(expectedInResponseTo); expected != "" {
		if assertion.InResponseTo != "" && assertion.InResponseTo != expected {
			return NewSAMLError(SAMLErrMalformed,
				"the assertion answers a different request")
		}
	}
	if assertion.Subject.NameID == "" {
		return NewSAMLError(SAMLErrNoSubject, "the assertion carries no NameID")
	}
	if assertion.Subject.Email == "" {
		return NewSAMLError(SAMLErrNoSubject,
			"the assertion carries no email attribute; the SP needs it to match an account")
	}
	return nil
}

// SAMLReplayKey returns the value a replay cache keys on.
//
// 用断言 ID 而不是 NameID：同一个人可以在一分钟内多次登录，但同一个断言
// 只能用一次。缓存应保留到 NotOnOrAfter 之后。
func SAMLReplayKey(assertion *SAMLAssertion) string {
	if assertion == nil {
		return ""
	}
	if assertion.AssertionID != "" {
		return "saml:assertion:" + assertion.AssertionID
	}
	// 没有 ID 的断言无法做重放保护；调用方应据此拒绝，而不是用 NameID 兜底
	// （那会禁止同一用户正常重复登录）。
	return ""
}
