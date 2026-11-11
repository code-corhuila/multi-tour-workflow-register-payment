package httpapi

import (
	"crypto/rsa"
	"errors"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
)

const (
	// tokenLeeway is the clock tolerance of ADR-004 D10.
	tokenLeeway         = 30 * time.Second
	tenantAdministrator = "TENANT_ADMINISTRATOR"
	collaborator        = "OPERATIONAL_COLLABORATOR"
	validateProofs      = "VALIDATE_PAYMENT_PROOFS"
	manageCash          = "MANAGE_CASH"
)

var (
	errMissingSubject = errors.New("token has no subject")
	errMalformedClaim = errors.New("token has a claim of the wrong type")
)

// Claims is what the entry route may know about the caller.
type Claims struct {
	Subject  string
	TenantID string
	// Access marks a token that serves one purpose only ("restricted", "password-change"); empty for an ordinary one.
	Access      string
	Roles       []string
	Permissions []string
}

// mayRegister says whether the caller may register a payment of that medium. The saga acts with a service credential, so
// the participants cannot tell who the person is: this is where the right is checked. A Tenant Administrator may; a
// Collaborator needs VALIDATE_PAYMENT_PROOFS for a transfer and MANAGE_CASH for cash. A token with no tenant never may.
func (c Claims) mayRegister(medium saga.Medium) bool {
	if c.Access != "" || c.TenantID == "" {
		return false
	}
	if slices.Contains(c.Roles, tenantAdministrator) {
		return true
	}
	if !slices.Contains(c.Roles, collaborator) {
		return false
	}
	if medium == saga.MediumCash {
		return slices.Contains(c.Permissions, manageCash)
	}
	return slices.Contains(c.Permissions, validateProofs)
}

// Verifier validates the access tokens of the security service with its public key only: this service never holds
// the private key (ADR-004 D5).
type Verifier struct{ key *rsa.PublicKey }

// NewVerifier expects a non-nil key; the composition root validates it at start-up.
func NewVerifier(key *rsa.PublicKey) *Verifier { return &Verifier{key} }

// Verify accepts exactly RS256 and requires exp and sub; every other algorithm, `none` included, is refused before
// the signature is looked at. A claim of the wrong type is refused instead of read as absent.
func (v *Verifier) Verify(raw string) (Claims, error) {
	token, err := jwt.Parse(raw,
		func(*jwt.Token) (any, error) { return v.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(tokenLeeway),
	)
	if err != nil {
		return Claims{}, err
	}
	subject, err := token.Claims.GetSubject()
	if err != nil || subject == "" {
		return Claims{}, errMissingSubject
	}
	claims := Claims{Subject: subject}
	body, _ := token.Claims.(jwt.MapClaims)
	for field, target := range map[string]*string{"tenantId": &claims.TenantID, "access": &claims.Access} {
		if mark, present := body[field]; present && mark != nil {
			text, isText := mark.(string)
			if !isText {
				return Claims{}, errMalformedClaim
			}
			*target = text
		}
	}
	for field, target := range map[string]*[]string{"roles": &claims.Roles, "permissions": &claims.Permissions} {
		list, err := textList(body[field])
		if err != nil {
			return Claims{}, err
		}
		*target = list
	}
	return claims, nil
}

func textList(listed any) ([]string, error) {
	if listed == nil {
		return nil, nil
	}
	list, isList := listed.([]any)
	if !isList {
		return nil, errMalformedClaim
	}
	var out []string
	for _, item := range list {
		name, isText := item.(string)
		if !isText {
			return nil, errMalformedClaim
		}
		out = append(out, name)
	}
	return out, nil
}
