package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// S256Challenge derives the PKCE S256 challenge for a verifier (RFC 7636 §4.2).
func S256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// VerifyPKCE reports whether verifier answers challenge under S256.
//
// S256 is the only method. "plain" sends the secret in the authorization
// request itself, where anyone who sees that request can redeem the code — the
// interception PKCE exists to stop — and OAuth 2.1 lets a server refuse it.
func VerifyPKCE(verifier, challenge string) bool {
	if !validVerifier(verifier) || !ValidChallenge(challenge) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(S256Challenge(verifier)), []byte(challenge)) == 1
}

// ValidChallenge reports whether challenge could be an S256 output: exactly 43
// unpadded base64url characters. Anything else can never be matched by a
// verifier, so it is refused at the authorization request instead of failing
// mysteriously at the token request.
func ValidChallenge(challenge string) bool {
	if len(challenge) != 43 {
		return false
	}
	for i := 0; i < len(challenge); i++ {
		if !isBase64URL(challenge[i]) {
			return false
		}
	}
	return true
}

// validVerifier applies RFC 7636 §4.1: 43–128 characters of
// [A-Z] / [a-z] / [0-9] / "-" / "." / "_" / "~".
func validVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !isBase64URL(c) && c != '.' && c != '~' {
			return false
		}
	}
	return true
}

func isBase64URL(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}
