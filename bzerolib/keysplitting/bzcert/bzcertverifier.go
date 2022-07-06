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

	initialIdTokenLifetime = time.Hour * 24 * 365 * 5 // 5 years
)

type BZCertVerifier struct {
	orgId        string
	ssoProvider  *oidc.Provider
	providerType ProviderType
}

// the claims we care about checking
type idTokenClaims struct {
	HD       string `json:"hd"`    // Google Org ID
	Nonce    string `json:"nonce"` // BastionZero-issued nonce
	TID      string `json:"tid"`   // Microsoft Tenant ID
	IssuedAt int64  `json:"iat"`   // Unix datetime of issuance
	Death    int64  `json:"exp"`   // Unix datetime of token expiry
}

type ProviderType string

const (
	Google    ProviderType = "google"
	Microsoft ProviderType = "microsoft"
	Okta      ProviderType = "okta"
	// Custom    ProviderType = "custom" // plan for custom IdP support
)

func NewVerifier(idpProvider string, idpOrgId string) (*BZCertVerifier, error) {
	// customIss := os.Getenv("CUSTOM_IDP")

	var issuerUrl string
	switch ProviderType(idpProvider) {
	case Google:
		issuerUrl = googleUrl
	case Microsoft:
		issuerUrl = fmt.Sprintf("%s/%s/v2.0", microsoftUrl, idpOrgId)
	case Okta:
		issuerUrl = fmt.Sprintf("https://%s.okta.com", idpOrgId)
	// case Custom:
	// 	issUrl = customIss
	default:
		return nil, fmt.Errorf("unrecognized SSO provider: %s", idpProvider)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(60*time.Second))
	defer cancel()

	provider, err := oidc.NewProvider(ctx, issuerUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to establish connection with SSO provider %s: %w", idpProvider, err)
	}

	return &BZCertVerifier{
		orgId:        idpOrgId,
		ssoProvider:  provider,
		providerType: ProviderType(idpProvider),
	}, nil
}

func (v *BZCertVerifier) Verify(bzcert *BZCert) (exp time.Time, err error) {
	if err = v.verifyInitialIdToken(bzcert.InitialIdToken, bzcert); err != nil {
		return exp, fmt.Errorf("error verifying initial id token: %w", err)
	} else if exp, err = v.verifyCurrentIdToken(bzcert.CurrentIdToken); err != nil {
		return exp, fmt.Errorf("error verifying current id token: %w", err)
	} else {
		return
	}
}

// this function verifies the current id token and will return that token's
// expiration time
func (v *BZCertVerifier) verifyCurrentIdToken(token string) (time.Time, error) {
	config := &oidc.Config{
		SkipClientIDCheck: true,
		SkipExpiryCheck:   false,
	}

	if claims, err := v.getTokenClaims(token, config); err != nil {
		return time.Time{}, err
	} else {
		return time.Unix(claims.Death, 0), nil
	}
}

// this function verifies the initial id token which requires checking whether
// the structured nonce is correctly formatted
func (v *BZCertVerifier) verifyInitialIdToken(token string, bzcert *BZCert) error {
	config := &oidc.Config{
		SkipClientIDCheck: true,
		SkipExpiryCheck:   true,
	}

	claims, err := v.getTokenClaims(token, config)
	if err != nil {
		return err
	}

	now := time.Now()
	iat := time.Unix(claims.IssuedAt, 0) // Confirmed both Microsoft and Google use Unix

	// the initial id token expires after a time specified by BastionZero
	if now.After(iat.Add(initialIdTokenLifetime)) {
		return fmt.Errorf("InitialIdToken Expired {Current Time = %v, Token iat = %v}", now, iat)
	}

	// Check if the structured nonce in id token is formatted correctly
	if err = v.verifyAuthNonce(bzcert, claims.Nonce); err != nil {
		return err
	}

	return nil
}

func (v *BZCertVerifier) getTokenClaims(idtoken string, config *oidc.Config) (*idTokenClaims, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(60*time.Second))
	defer cancel()

	// This checks formatting and signature validity
	verifier := v.ssoProvider.Verifier(config)
	token, err := verifier.Verify(ctx, idtoken)
	if err != nil {
		return nil, fmt.Errorf("failed to verify id token with SSO provider: %w", err)
	}

	// Extract claims from token
	var claims idTokenClaims
	if err = token.Claims(&claims); err != nil {
		return nil, fmt.Errorf("error parsing the ID Token: %s", err)
	}

	// Only validate org claim if there is an orgId associated with this agent. This will be empty
	// for orgs associated with a personal gsuite/microsoft account. We do not need to check against
	// anything for Okta, because Okta creates a specific issuer url for every org meaning that by
	// virtue of getting the claims, we are assured it's for the specific Okta tenant.
	switch v.providerType {
	case Google:
		if v.orgId != claims.HD {
			return nil, fmt.Errorf("user's OrgId does not match target's expected Google HD")
		}
	case Microsoft:
		if v.orgId != claims.TID {
			return nil, fmt.Errorf("user's OrgId does not match target's expected Microsoft tid")
		}
	}

	return &claims, nil
}

// This function takes in the BZECert, extracts all fields for verifying the AuthNonce (sent as part of
// the ID Token).  Returns nil if nonce is verified, else returns an error.
// Nonce should equal ClientPublicKey + SignatureOnRandomValue + RandomValue, where the signature is valid.
func (v *BZCertVerifier) verifyAuthNonce(bzcert *BZCert, authNonce string) error {
	nonce := bzcert.ClientPublicKey + bzcert.SignatureOnRand + bzcert.Rand
	hash := sha3.Sum256([]byte(nonce))
	nonceHash := base64.StdEncoding.EncodeToString(hash[:])

	// check nonce is equal to what is expected
	if authNonce != nonceHash {
		return fmt.Errorf("nonce in ID token does not match calculated nonce hash")
	}

	decodedRand, err := base64.StdEncoding.DecodeString(bzcert.Rand)
	if err != nil {
		return fmt.Errorf("BZCert Rand is not base64 encoded")
	}

	randHashBits := sha3.Sum256([]byte(decodedRand))
	sigBits, _ := base64.StdEncoding.DecodeString(bzcert.SignatureOnRand)

	pubKeyBits, _ := base64.StdEncoding.DecodeString(bzcert.ClientPublicKey)
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
