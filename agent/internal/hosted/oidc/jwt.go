// Package oidc is a small OpenID Connect relying-party client: discovery, authorization-code flow with PKCE,
// ID token verification (issuer, audience, nonce, expiry, signature via JWKS), refresh, and confidential-client
// authentication with a client secret or a private-key JWT. It uses only the standard library.
package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Claims are the verified claims of an ID token.
type Claims struct {
	Issuer   string
	Subject  string
	Audience []string
	Expiry   time.Time
	Nonce    string
	Raw      map[string]any
}

// String returns a string claim ("" when absent or not a string).
func (c *Claims) String(name string) string {
	s, _ := c.Raw[name].(string)
	return s
}

// Strings returns a string-list claim; a single string becomes a one-element list.
func (c *Claims) Strings(name string) []string {
	switch v := c.Raw[name].(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func b64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err1 := b64(k.N)
		e, err2 := b64(k.E)
		if err1 != nil || err2 != nil || len(n) == 0 || len(e) == 0 {
			return nil, errors.New("bad RSA key")
		}
		ei := new(big.Int).SetBytes(e)
		if !ei.IsInt64() || ei.Int64() > 1<<31 {
			return nil, errors.New("bad RSA exponent")
		}
		pk := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(ei.Int64())}
		if pk.N.BitLen() < 2048 {
			return nil, errors.New("RSA key too small")
		}
		return pk, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, errors.New("unsupported curve")
		}
		x, err1 := b64(k.X)
		y, err2 := b64(k.Y)
		if err1 != nil || err2 != nil {
			return nil, errors.New("bad EC key")
		}
		pk := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !curve.IsOnCurve(pk.X, pk.Y) {
			return nil, errors.New("EC point not on curve")
		}
		return pk, nil
	}
	return nil, fmt.Errorf("unsupported key type %q", k.Kty)
}

var algHash = map[string]crypto.Hash{
	"RS256": crypto.SHA256, "RS384": crypto.SHA384, "RS512": crypto.SHA512,
	"PS256": crypto.SHA256, "PS384": crypto.SHA384, "PS512": crypto.SHA512,
	"ES256": crypto.SHA256, "ES384": crypto.SHA384, "ES512": crypto.SHA512,
}

func verifySig(alg string, pub crypto.PublicKey, signingInput string, sig []byte) error {
	h, ok := algHash[alg]
	if !ok {
		return fmt.Errorf("unsupported alg %q", alg)
	}
	hh := h.New()
	hh.Write([]byte(signingInput))
	sum := hh.Sum(nil)
	switch alg[:2] {
	case "RS":
		pk, ok := pub.(*rsa.PublicKey)
		if !ok {
			return errors.New("key type does not match alg")
		}
		return rsa.VerifyPKCS1v15(pk, h, sum, sig)
	case "PS":
		pk, ok := pub.(*rsa.PublicKey)
		if !ok {
			return errors.New("key type does not match alg")
		}
		return rsa.VerifyPSS(pk, h, sum, sig, nil)
	case "ES":
		pk, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("key type does not match alg")
		}
		size := (pk.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*size {
			return errors.New("bad ECDSA signature length")
		}
		if !ecdsa.Verify(pk, sum, new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])) {
			return errors.New("bad signature")
		}
		return nil
	}
	return errors.New("unsupported alg")
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// splitJWT returns the header, the raw signing input, the signature and the claims map.
func splitJWT(raw string) (jwtHeader, string, []byte, map[string]any, error) {
	var h jwtHeader
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return h, "", nil, nil, errors.New("malformed token")
	}
	hb, err := b64(parts[0])
	if err != nil || json.Unmarshal(hb, &h) != nil {
		return h, "", nil, nil, errors.New("malformed token header")
	}
	cb, err := b64(parts[1])
	if err != nil {
		return h, "", nil, nil, errors.New("malformed token payload")
	}
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		return h, "", nil, nil, errors.New("malformed token claims")
	}
	sig, err := b64(parts[2])
	if err != nil {
		return h, "", nil, nil, errors.New("malformed token signature")
	}
	return h, parts[0] + "." + parts[1], sig, claims, nil
}

func numericDate(v any) (time.Time, bool) {
	f, ok := v.(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}
