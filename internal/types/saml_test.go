package types

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// samlTestCert generates a self-signed certificate for signature tests.
func samlTestCert(t *testing.T) (*x509.Certificate, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-idp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return cert, key, pemText
}

// signSAML signs bytes with SHA-256.
func signSAML(t *testing.T, key *rsa.PrivateKey, payload []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(payload)
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return signature
}

// TestParseIdPCertificatePEMRejectsGarbage keeps a bad config visible.
func TestParseIdPCertificatePEMRejectsGarbage(t *testing.T) {
	_, err := ParseIdPCertificatePEM("not a certificate")
	require.Error(t, err)
	assert.Equal(t, SAMLErrUnknownCertificate, err.(*SAMLError).Code)

	_, err = ParseIdPCertificatePEM("")
	require.Error(t, err)
}

// TestVerifySAMLSignatureAcceptsAValidSignature is the core crypto check.
func TestVerifySAMLSignatureAcceptsAValidSignature(t *testing.T) {
	cert, key, _ := samlTestCert(t)
	payload := []byte("<Assertion>payload</Assertion>")
	signature := signSAML(t, key, payload)

	require.NoError(t, VerifySAMLSignature(cert, SAMLSignatureInput{
		SignedBytes: payload, Signature: signature, Algorithm: SAMLAlgRSASHA256,
	}, false))
}

// TestVerifySAMLSignatureRejectsTamperedBytes is the reason signatures exist.
func TestVerifySAMLSignatureRejectsTamperedBytes(t *testing.T) {
	cert, key, _ := samlTestCert(t)
	signature := signSAML(t, key, []byte("<Assertion>original</Assertion>"))

	err := VerifySAMLSignature(cert, SAMLSignatureInput{
		SignedBytes: []byte("<Assertion>tampered</Assertion>"),
		Signature:   signature,
		Algorithm:   SAMLAlgRSASHA256,
	}, false)
	require.Error(t, err)
	assert.Equal(t, SAMLErrBadSignature, err.(*SAMLError).Code)
}

// TestVerifySAMLSignatureRefusesSHA1ByDefault is the deprecation stance.
func TestVerifySAMLSignatureRefusesSHA1ByDefault(t *testing.T) {
	cert, key, _ := samlTestCert(t)
	payload := []byte("payload")
	signature := signSAML(t, key, payload)

	err := VerifySAMLSignature(cert, SAMLSignatureInput{
		SignedBytes: payload, Signature: signature, Algorithm: SAMLAlgRSASHA1,
	}, false)
	require.Error(t, err)
	assert.Contains(t, err.(*SAMLError).Message, "rsa-sha1")

	// The caller may opt in for a legacy IdP — with a signature that is
	// actually SHA-1, not a SHA-256 one relabelled.
	sha1Digest := sha1.Sum(payload)
	sha1Signature, signErr := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, sha1Digest[:])
	require.NoError(t, signErr)
	require.NoError(t, VerifySAMLSignature(cert, SAMLSignatureInput{
		SignedBytes: payload, Signature: sha1Signature, Algorithm: SAMLAlgRSASHA1,
	}, true))
}

// TestVerifySAMLSignatureRequiresACertificate is fail-closed.
func TestVerifySAMLSignatureRequiresACertificate(t *testing.T) {
	err := VerifySAMLSignature(nil, SAMLSignatureInput{
		SignedBytes: []byte("x"), Signature: []byte("y"),
	}, false)
	require.Error(t, err)
	assert.Equal(t, SAMLErrUnknownCertificate, err.(*SAMLError).Code)
}

// TestVerifySAMLSignatureRejectsEmptySignature covers the missing-signature case.
func TestVerifySAMLSignatureRejectsEmptySignature(t *testing.T) {
	cert, _, _ := samlTestCert(t)
	err := VerifySAMLSignature(cert, SAMLSignatureInput{SignedBytes: []byte("x")}, false)
	require.Error(t, err)
	assert.Equal(t, SAMLErrNoSignature, err.(*SAMLError).Code)
}

// samlResponseXML builds a Response document for parsing tests.
func samlResponseXML(inResponseTo, audience, email string) string {
	return `<?xml version="1.0"?>
<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol" ID="resp-1" InResponseTo="` + inResponseTo + `">
  <Issuer>https://idp.example.com</Issuer>
  <Status><StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></Status>
  <Assertion ID="assert-1">
    <Issuer>https://idp.example.com</Issuer>
    <Subject><NameID>user-123</NameID></Subject>
    <Conditions NotBefore="2026-11-04T09:00:00Z" NotOnOrAfter="2026-11-04T11:00:00Z">
      <AudienceRestriction><Audience>` + audience + `</Audience></AudienceRestriction>
    </Conditions>
    <AuthnStatement SessionIndex="session-1"/>
    <AttributeStatement>
      <Attribute Name="email"><AttributeValue>` + email + `</AttributeValue></Attribute>
      <Attribute Name="displayName"><AttributeValue>张三</AttributeValue></Attribute>
      <Attribute Name="groups">
        <AttributeValue>engineering</AttributeValue>
        <AttributeValue>reviewers</AttributeValue>
      </Attribute>
    </AttributeStatement>
  </Assertion>
</Response>`
}

// TestParseSAMLResponseExtractsTheAssertion covers parsing.
func TestParseSAMLResponseExtractsTheAssertion(t *testing.T) {
	assertion, err := ParseSAMLResponse([]byte(samlResponseXML("req-1", "weknora-sp", "zhang@example.com")))
	require.NoError(t, err)

	assert.Equal(t, "user-123", assertion.Subject.NameID)
	assert.Equal(t, "zhang@example.com", assertion.Subject.Email)
	assert.Equal(t, "张三", assertion.Subject.DisplayName)
	assert.Equal(t, []string{"engineering", "reviewers"}, assertion.Subject.Groups)
	assert.Equal(t, "session-1", assertion.Subject.SessionIndex)
	assert.Equal(t, "https://idp.example.com", assertion.Issuer)
	assert.Equal(t, "weknora-sp", assertion.Audience)
	assert.Equal(t, "req-1", assertion.InResponseTo)
	assert.Equal(t, "assert-1", assertion.AssertionID)
	assert.Equal(t, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), assertion.NotBefore.UTC())
}

// TestParseSAMLResponseRefusesEnvelopedSignature is the documented limit: this
// SP cannot canonicalise XML, so it refuses rather than skipping the check.
func TestParseSAMLResponseRefusesEnvelopedSignature(t *testing.T) {
	doc := `<Response><Issuer>idp</Issuer><Assertion ID="a">
	  <Issuer>idp</Issuer>
	  <Signature xmlns="http://www.w3.org/2000/09/xmldsig#"><SignatureValue>abc</SignatureValue></Signature>
	</Assertion></Response>`
	_, err := ParseSAMLResponse([]byte(doc))
	require.Error(t, err)
	assert.Equal(t, SAMLErrEnvelopedSignature, err.(*SAMLError).Code)
	assert.Contains(t, err.(*SAMLError).Message, "canonicalisation")
}

// TestParseSAMLResponseRefusesEncryptedAssertion covers the other unsupported form.
func TestParseSAMLResponseRefusesEncryptedAssertion(t *testing.T) {
	doc := `<Response><Issuer>idp</Issuer><EncryptedAssertion><Data/></EncryptedAssertion></Response>`
	_, err := ParseSAMLResponse([]byte(doc))
	require.Error(t, err)
	assert.Equal(t, SAMLErrEncrypted, err.(*SAMLError).Code)
}

// TestParseSAMLResponseWithoutAssertion covers the empty case.
func TestParseSAMLResponseWithoutAssertion(t *testing.T) {
	_, err := ParseSAMLResponse([]byte(`<Response><Issuer>idp</Issuer></Response>`))
	require.Error(t, err)
	assert.Equal(t, SAMLErrMissingAssertion, err.(*SAMLError).Code)
}

// TestDecodeSAMLResponseBase64RejectsOversizedPayloads bounds the input.
func TestDecodeSAMLResponseBase64RejectsOversizedPayloads(t *testing.T) {
	huge := make([]byte, SAMLMaxResponseBytes*2)
	_, err := DecodeSAMLResponseBase64(base64.StdEncoding.EncodeToString(huge))
	require.Error(t, err)
	assert.Equal(t, SAMLErrTooLarge, err.(*SAMLError).Code)
}

// TestDecodeSAMLResponseBase64AcceptsValidInput covers the happy path.
func TestDecodeSAMLResponseBase64AcceptsValidInput(t *testing.T) {
	raw, err := DecodeSAMLResponseBase64(base64.StdEncoding.EncodeToString([]byte("<Response/>")))
	require.NoError(t, err)
	assert.Equal(t, "<Response/>", string(raw))

	_, err = DecodeSAMLResponseBase64("!!!not base64!!!")
	require.Error(t, err)
}

// TestValidateSAMLAssertionChecksIssuerBeforeTime is the ordering rule: an
// assertion from the wrong IdP must not be reported as merely expired.
func TestValidateSAMLAssertionChecksIssuerBeforeTime(t *testing.T) {
	config := SAMLConfig{EntityID: "weknora-sp", IdPEntityID: "https://idp.example.com"}
	expired := &SAMLAssertion{
		Issuer:       "https://evil.example.com",
		Audience:     "weknora-sp",
		NotOnOrAfter: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Subject:      SAMLSubject{NameID: "u", Email: "e@example.com"},
	}
	err := ValidateSAMLAssertion(config, expired, time.Now(), "")
	require.Error(t, err)
	assert.Equal(t, SAMLErrIssuerMismatch, err.(*SAMLError).Code,
		"the issuer is checked before the clock so the real problem is reported")
}

// TestValidateSAMLAssertionEnforcesTheWindow covers NotBefore / NotOnOrAfter.
func TestValidateSAMLAssertionEnforcesTheWindow(t *testing.T) {
	config := SAMLConfig{
		EntityID: "weknora-sp", IdPEntityID: "idp",
		ClockSkew: 0, // falls back to the default 2 minutes
	}
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	base := SAMLAssertion{
		Issuer: "idp", Audience: "weknora-sp",
		Subject:   SAMLSubject{NameID: "u", Email: "e@example.com"},
		NotBefore: now.Add(-time.Minute), NotOnOrAfter: now.Add(time.Minute),
	}
	require.NoError(t, ValidateSAMLAssertion(config, &base, now, ""))

	future := base
	future.NotBefore = now.Add(time.Hour)
	err := ValidateSAMLAssertion(config, &future, now, "")
	require.Error(t, err)
	assert.Equal(t, SAMLErrNotYetValid, err.(*SAMLError).Code)

	past := base
	past.NotOnOrAfter = now.Add(-time.Hour)
	err = ValidateSAMLAssertion(config, &past, now, "")
	require.Error(t, err)
	assert.Equal(t, SAMLErrExpired, err.(*SAMLError).Code)

	// Inside the skew is still accepted.
	justExpired := base
	justExpired.NotOnOrAfter = now.Add(-time.Second)
	require.NoError(t, ValidateSAMLAssertion(config, &justExpired, now, ""))
}

// TestValidateSAMLAssertionChecksAudience keeps an assertion for another SP out.
func TestValidateSAMLAssertionChecksAudience(t *testing.T) {
	config := SAMLConfig{EntityID: "weknora-sp"}
	assertion := &SAMLAssertion{
		Audience: "another-sp",
		Subject:  SAMLSubject{NameID: "u", Email: "e@example.com"},
	}
	err := ValidateSAMLAssertion(config, assertion, time.Now(), "")
	require.Error(t, err)
	assert.Equal(t, SAMLErrAudienceMismatch, err.(*SAMLError).Code)
}

// TestValidateSAMLAssertionRequiresEmail is an SP requirement, not an IdP one:
// the platform matches accounts by email.
func TestValidateSAMLAssertionRequiresEmail(t *testing.T) {
	assertion := &SAMLAssertion{Subject: SAMLSubject{NameID: "u"}}
	err := ValidateSAMLAssertion(SAMLConfig{}, assertion, time.Now(), "")
	require.Error(t, err)
	assert.Equal(t, SAMLErrNoSubject, err.(*SAMLError).Code)
	assert.Contains(t, err.(*SAMLError).Message, "email")
}

// TestValidateSAMLAssertionChecksInResponseTo covers the CSRF guard.
func TestValidateSAMLAssertionChecksInResponseTo(t *testing.T) {
	assertion := &SAMLAssertion{
		InResponseTo: "req-a",
		Subject:      SAMLSubject{NameID: "u", Email: "e@example.com"},
	}
	err := ValidateSAMLAssertion(SAMLConfig{}, assertion, time.Now(), "req-b")
	require.Error(t, err)
	assert.Contains(t, err.(*SAMLError).Message, "different request")

	require.NoError(t, ValidateSAMLAssertion(SAMLConfig{}, assertion, time.Now(), "req-a"))
}

// TestSAMLReplayKeyUsesTheAssertionID keeps one assertion usable once.
func TestSAMLReplayKeyUsesTheAssertionID(t *testing.T) {
	assert.Equal(t, "saml:assertion:a-1",
		SAMLReplayKey(&SAMLAssertion{AssertionID: "a-1"}))
	// Without an ID there is no replay protection; the caller must refuse.
	assert.Empty(t, SAMLReplayKey(&SAMLAssertion{Subject: SAMLSubject{NameID: "u"}}))
	assert.Empty(t, SAMLReplayKey(nil))
}

// TestSAMLBindingValid covers the enum guard.
func TestSAMLBindingValid(t *testing.T) {
	assert.True(t, SAMLBindingPOST.Valid())
	assert.True(t, SAMLBindingRedirect.Valid())
	assert.False(t, SAMLBinding("artifact").Valid())
}
