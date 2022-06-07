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

type BZCertVerifier struct {
	orgId       string
	orgProvider ProviderType
	issUrl      string
}

// idTokenClaims are the claims we care about checking
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
)

func NewBZCertVerifier(idpProvider string, idpOrgId string) (*BZCertVerifier, error) {
	issUrl := ""
	switch ProviderType(idpProvider) {
	case Google:
		issUrl = googleUrl
	case Microsoft:
		issUrl = getMicrosoftIssUrl(idpOrgId)
	case Okta:
		issUrl = "https://" + idpOrgId + ".okta.com"
	default:
		return nil, fmt.Errorf("unrecognized OIDC provider: %s", idpProvider)
	}

	return &BZCertVerifier{
		orgId:       idpOrgId,
		orgProvider: ProviderType(idpProvider),
		issUrl:      issUrl,
	}, nil
}

// Verify verifies the user's bzcert. If verification succeeds, then we return
// the expiration time and hash of the BZCert. Otherwise, an error is returned.
func (v *BZCertVerifier) Verify(bzCert BZCert) (string, time.Time, error) {
	// Verify InitialIdToken
	if err := v.verifyInitialIdToken(bzCert); err != nil {
		return "", time.Time{}, fmt.Errorf("error verifying initial id token: %w", err)
	}

	// Verify CurrentIdToken
	if exp, err := v.verifyCurrentIdToken(bzCert); err != nil {
		return "", time.Time{}, fmt.Errorf("error verifying current id token: %w", err)
	} else {
		if hash, ok := bzCert.Hash(); ok {
			return hash, exp, err
		} else {
			return "", time.Time{}, fmt.Errorf("failed to hash BZCert")
		}
	}
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

func getDefaultOidcConfig() *oidc.Config {
	return &oidc.Config{
		SkipClientIDCheck: true,
	}
}

// verifyCurrentIdToken verifies that the bzCert's CurrentIdToken field is
// correct. It returns the expiration time of the CurrentIdToken if it validates
// successfully. Otherwise, an error is returned.
func (v *BZCertVerifier) verifyCurrentIdToken(bzCert BZCert) (time.Time, error) {
	// Unlike verifyInitialIdToken(), we do not set SkipExpiryCheck to true. We
	// want to check if CurrentIdToken is expired because it is meant to be a
	// short-lived token. The user can request a new CurrentIdToken by using
	// their IdP's issued refresh token.
	oidcConfig := getDefaultOidcConfig()

	claims, err := v.verifyIdToken(bzCert.CurrentIdToken, oidcConfig)
	if err != nil {
		return time.Time{}, err
	}

	// Everything validated!
	return time.Unix(claims.Death, 0), nil
}

// verifyInitialIdToken verifies that the bzCert's InitialIdToken field is
// correct. It returns nil if it validates successfully. Otherwise, an error is
// returned.
func (v *BZCertVerifier) verifyInitialIdToken(bzCert BZCert) error {
	oidcConfig := getDefaultOidcConfig()
	// We skip the expiry check for InitialIdToken because it is longer-lived
	// than the current id token. The InitialIdToken is only updated when the
	// user goes through the *interactive* OIDC login procedure.
	oidcConfig.SkipExpiryCheck = true

	if claims, err := v.verifyIdToken(bzCert.InitialIdToken, oidcConfig); err != nil {
		return err
	} else {
		// Otherwise, if error is nil, then id_token is signed by the issuer and
		// valid for this org.

		// Now do additional checks on the claims specific to initialIdToken.

		// Manual check to see if InitialIdToken is expired
		now := time.Now()
		iat := time.Unix(claims.IssuedAt, 0) // Confirmed both Microsoft and Google use Unix
		if now.After(iat.Add(bzCustomTokenLifetime)) {
			return fmt.Errorf("InitialIdToken Expired {Current Time = %v, Token iat = %v}", now, iat)
		}

		// Verify nonce claim is valid
		if err = verifyAuthNonce(claims.Nonce, bzCert.ClientPublicKey, bzCert.SignatureOnRand, bzCert.Rand); err != nil {
			return err
		}

		// Everything validated!
		return nil
	}
}

// This function verifies id_tokens
func (v *BZCertVerifier) verifyIdToken(idToken string, oidcConfig *oidc.Config) (idTokenClaims, error) {
	// If there is no issuer URL, skip id token verification
	// Provider isn't stored for single-player orgs
	if v.issUrl == "" {
		return idTokenClaims{}, nil
	}

	// Setup OIDC provider
	ctx := context.TODO() // Gives us non-nil empty context
	provider, err := oidc.NewProvider(ctx, v.issUrl)
	if err != nil {
		return idTokenClaims{}, fmt.Errorf("invalid OIDC provider: %s", err)
	}

	// This checks formatting and signature validity
	verifier := provider.Verifier(oidcConfig)
	token, err := verifier.Verify(ctx, idToken)
	if err != nil {
		return idTokenClaims{}, fmt.Errorf("ID Token verification error: %s", err)
	}

	// Verify Claims
	var claims idTokenClaims
	if err := token.Claims(&claims); err != nil {
		return idTokenClaims{}, fmt.Errorf("error parsing the ID Token: %s", err)
	}

	// Only validate org claim if there is an orgId associated with this agent.
	// This will be empty for orgs associated with a personal gsuite/microsoft
	// account. We do not need to check against anything for Okta, because Okta
	// creates a specific issuer url for every org meaning that by virtue of
	// getting the claims, we are assured it's for the specific Okta tenant.
	switch v.orgProvider {
	case Google:
		if v.orgId != claims.HD {
			return idTokenClaims{}, fmt.Errorf("user's OrgId does not match target's expected Google HD")
		}
	case Microsoft:
		if v.orgId != claims.TID {
			return idTokenClaims{}, fmt.Errorf("user's OrgId does not match target's expected Microsoft tid")
		}
	}

	return claims, nil
}

// verifyAuthNonce verifies that the nonceClaim matches the expected nonce
// (derived from clientPubKey+sigOnRand+rand), and that sigOnRand is a valid
// signature on rand
func verifyAuthNonce(nonceClaim string, clientPubKey string, sigOnRand string, rand string) error {
	nonce := clientPubKey + sigOnRand + rand
	hash := sha3.Sum256([]byte(nonce))
	nonceHash := base64.StdEncoding.EncodeToString(hash[:])

	// check nonce is equal to what is expected
	if nonceClaim != nonceHash {
		return fmt.Errorf("nonce in ID token does not match calculated nonce hash")
	}

	decodedRand, err := base64.StdEncoding.DecodeString(rand)
	if err != nil {
		return fmt.Errorf("BZCert Rand is not base64 encoded")
	}

	randHashBits := sha3.Sum256([]byte(decodedRand))
	sigBits, _ := base64.StdEncoding.DecodeString(sigOnRand)

	pubKeyBits, _ := base64.StdEncoding.DecodeString(clientPubKey)
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
