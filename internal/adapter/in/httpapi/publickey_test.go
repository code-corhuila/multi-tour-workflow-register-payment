package httpapi_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/adapter/in/httpapi"
)

func pemOf(t *testing.T, blockType string, der []byte) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}

// The public key reaches the service by configuration (ADR-004 D5): PKIX or PKCS#1, RSA of at least 2048 bits.
func TestC_ParsePublicKey_reads_pkix_and_pkcs1(t *testing.T) {
	pkix, err := x509.MarshalPKIXPublicKey(&signer.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"pkix":  pemOf(t, "PUBLIC KEY", pkix),
		"pkcs1": pemOf(t, "RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&signer.PublicKey)),
	} {
		key, err := httpapi.ParsePublicKey(data)
		if err != nil || key.N.Cmp(signer.N) != 0 {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestC_ParsePublicKey_refuses_what_is_not_a_strong_rsa_key(t *testing.T) {
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	smallDER, _ := x509.MarshalPKIXPublicKey(&small.PublicKey)
	for name, data := range map[string][]byte{
		"not pem":  []byte("not a key"),
		"garbage":  pemOf(t, "PUBLIC KEY", []byte{1, 2, 3}),
		"too weak": pemOf(t, "PUBLIC KEY", smallDER),
	} {
		if _, err := httpapi.ParsePublicKey(data); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
