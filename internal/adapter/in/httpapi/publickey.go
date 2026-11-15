package httpapi

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
)

const minKeyBits = 2048

// ParsePublicKey reads the RSA public key of the security service from PEM (PKIX or PKCS#1).
func ParsePublicKey(data []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("the public key is not PEM")
	}
	var key any
	var err error
	if strings.Contains(block.Type, "RSA") {
		key, err = x509.ParsePKCS1PublicKey(block.Bytes)
	} else {
		key, err = x509.ParsePKIXPublicKey(block.Bytes)
	}
	if err != nil {
		return nil, errors.New("the public key cannot be read")
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok || rsaKey.N.BitLen() < minKeyBits {
		return nil, errors.New("the public key must be RSA of at least 2048 bits")
	}
	return rsaKey, nil
}
