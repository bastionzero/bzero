package bzcert

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	ed "crypto/ed25519"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/crypto/sha3"
)

const (
	googleUrl    = "https://accounts.google.com"
	microsoftUrl = "https://login.microsoftonline.com"

	// this is the tenant id Microsoft uses when the account is a personal account (not a work/school account)
	// https://docs.microsoft.com/en-us/azure/active-directory/develop/id-tokens#payload-claims)
	microsoftPersonalAccountTenantId = "9188040d-6c67-4c5b-b112-36a304b66dad"

	bzCustomTokenLifetime = time.Hour * 24 * 365 * 5 // 5 years
)

type IBZCertVerifier interface {
	VerifyIdToken(idtoken string, skipExpiry bool, verifyNonce bool) (time.Time, error)
}

type BZCertVerifier struct {
	orgId       string
	orgProvider ProviderType
	issUrl      string
	cert        *BZCert
}

type ProviderType string

const (
	Google    ProviderType = "google"
	Microsoft ProviderType = "microsoft"
	Okta      ProviderType = "okta"
	// Custom    ProviderType = "custom" // TODO: support custom IdPs
	None ProviderType = "None"
)

func NewBZCertVerifier(bzcert *BZCert, idpProvider string, idpOrgId string) (IBZCertVerifier, error) {
	// customIss := os.Getenv("CUSTOM_IDP")

	issUrl := ""
	switch ProviderType(idpProvider) {
	case Google:
		issUrl = googleUrl
	case Microsoft:
		issUrl = getMicrosoftIssUrl(idpOrgId)
	case Okta:
		issUrl = "https://" + idpOrgId + ".okta.com"
	// case Custom:
	// 	issUrl = customIss
	default:
		return &BZCertVerifier{}, fmt.Errorf("unrecognized OIDC provider: %s", idpProvider)
	}

	return &BZCertVerifier{
		orgId:       idpOrgId,
		orgProvider: ProviderType(idpProvider),
		issUrl:      issUrl,
		cert:        bzcert,
	}, nil
}

func getMicrosoftIssUrl(orgId string) string {
	// Handles personal accounts by using microsoftPersonalAccountTenantId as the tenantId
	// see https://github.com/coreos/go-oidc/issues/121
	tenantId := ""
	if orgId == "None" {
		tenantId = microsoftPersonalAccountTenantId
	} else {
		tenantId = orgId
	}

	return microsoftUrl + "/" + tenantId + "/v2.0"
}

// This function verifies id_tokens
func (u *BZCertVerifier) VerifyIdToken(idtoken string, skipExpiry bool, verifyNonce bool) (time.Time, error) {
	// Verify Token Signature

	// If there is no issuer URL, skip id token verification
	// Provider isn't stored for single-player orgs
	if u.issUrl == "" {
		return time.Now().Add(bzCustomTokenLifetime), nil
	}

	ctx := context.TODO() // Gives us non-nil empty context
	config := &oidc.Config{
		SkipClientIDCheck: true,
		SkipExpiryCheck:   skipExpiry,
		// SupportedSigningAlgs: []string{RS256, ES512},
	}

	provider, err := oidc.NewProvider(ctx, u.issUrl)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid OIDC provider: %s", err)
	}

	// This checks formatting and signature validity
	verifier := provider.Verifier(config)
	token, err := verifier.Verify(ctx, idtoken)
	if err != nil {
		return time.Time{}, fmt.Errorf("ID Token verification error: %s", err)
	}

	// Verify Claims

	// the claims we care about checking
	var claims struct {
		HD       string `json:"hd"`    // Google Org ID
		Nonce    string `json:"nonce"` // BastionZero-issued nonce
		TID      string `json:"tid"`   // Microsoft Tenant ID
		IssuedAt int64  `json:"iat"`   // Unix datetime of issuance
		Death    int64  `json:"exp"`   // Unix datetime of token expiry
	}

	if err := token.Claims(&claims); err != nil {
		return time.Time{}, fmt.Errorf("error parsing the ID Token: %s", err)
	}

	// Manual check to see if InitialIdToken is expired
	if skipExpiry {
		now := time.Now()
		iat := time.Unix(claims.IssuedAt, 0) // Confirmed both Microsoft and Google use Unix
		if now.After(iat.Add(bzCustomTokenLifetime)) {
			return time.Time{}, fmt.Errorf("InitialIdToken Expired {Current Time = %v, Token iat = %v}", now, iat)
		}
	}

	// Check if Nonce in ID token is formatted correctly
	if verifyNonce {
		if err = u.verifyAuthNonce(claims.Nonce); err != nil {
			return time.Time{}, err
		}
	}

	// Only validate org claim if there is an orgId associated with this agent. This will be empty
	// for orgs associated with a personal gsuite/microsoft account. We do not need to check against
	// anything for Okta, because Okta creates a specific issuer url for every org meaning that by
	// virtue of getting the claims, we are assured it's for the specific Okta tenant.
	switch u.orgProvider {
	case Google:
		if u.orgId != claims.HD {
			return time.Time{}, fmt.Errorf("user's OrgId does not match target's expected Google HD")
		}
	case Microsoft:
		if u.orgId != claims.TID {
			return time.Time{}, fmt.Errorf("user's OrgId does not match target's expected Microsoft tid")
		}
	}

	return time.Unix(claims.Death, 0), nil
}

// This function takes in the BZECert, extracts all fields for verifying the AuthNonce (sent as part of
// the ID Token).  Returns nil if nonce is verified, else returns an error.
// Nonce should equal ClientPublicKey + SignatureOnRandomValue + RandomValue, where the signature is valid.
func (b *BZCertVerifier) verifyAuthNonce(authNonce string) error {
	nonce := b.cert.ClientPublicKey + b.cert.SignatureOnRand + b.cert.Rand
	hash := sha3.Sum256([]byte(nonce))
	nonceHash := base64.StdEncoding.EncodeToString(hash[:])

	// check nonce is equal to what is expected
	if authNonce != nonceHash {
		return fmt.Errorf("nonce in ID token does not match calculated nonce hash")
	}

	decodedRand, err := base64.StdEncoding.DecodeString(b.cert.Rand)
	if err != nil {
		return fmt.Errorf("BZCert Rand is not base64 encoded")
	}

	randHashBits := sha3.Sum256([]byte(decodedRand))
	sigBits, _ := base64.StdEncoding.DecodeString(b.cert.SignatureOnRand)

	pubKeyBits, _ := base64.StdEncoding.DecodeString(b.cert.ClientPublicKey)
	if len(pubKeyBits) != 32 {
		return fmt.Errorf("public key has invalid length %v", len(pubKeyBits))
	}
	pubkey := ed.PublicKey(pubKeyBits)

	if ok := ed.Verify(pubkey, randHashBits[:], sigBits); ok {
		return nil
	} else {
		return fmt.Errorf("failed to verify signature on rand")
	}
}
