package oauth

import (
	"strings"
	"testing"
)

// RFC 7636 Appendix B. A hand-rolled S256 that gets any step wrong — hex instead
// of base64url, padding left on, the challenge hashed twice — fails this row.
const (
	rfcVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	rfcChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

func TestVerifyPKCE(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		verifier  string
		challenge string
		want      bool
	}{
		{"the RFC 7636 vector", rfcVerifier, rfcChallenge, true},
		{"a different verifier", strings.Repeat("a", 43), rfcChallenge, false},

		// The plain method compares verifier to challenge directly. Accepting it
		// would let anyone who saw the authorization request redeem the code,
		// which is the attack PKCE exists to stop.
		{"plain: verifier equal to challenge", rfcChallenge, rfcChallenge, false},

		{"empty verifier", "", rfcChallenge, false},
		{"empty challenge", rfcVerifier, "", false},

		// RFC 7636 §4.1: 43 to 128 characters from the unreserved set.
		{"verifier too short", rfcVerifier[:42], rfcChallenge, false},
		{"verifier too long", strings.Repeat("a", 129), rfcChallenge, false},
		{"verifier with a disallowed character", rfcVerifier[:42] + "+", rfcChallenge, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := VerifyPKCE(tc.verifier, tc.challenge); got != tc.want {
				t.Errorf("VerifyPKCE(%q, %q) = %v, want %v", tc.verifier, tc.challenge, got, tc.want)
			}
		})
	}
}

func TestVerifierAtTheLengthBoundsIsAccepted(t *testing.T) {
	t.Parallel()

	for _, n := range []int{43, 128} {
		v := strings.Repeat("A", n)
		if !VerifyPKCE(v, S256Challenge(v)) {
			t.Errorf("a %d-character verifier was refused", n)
		}
	}
}

func TestValidChallenge(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"an S256 challenge", rfcChallenge, true},
		{"empty", "", false},
		// An S256 challenge is always exactly 43 base64url characters. Anything
		// else was produced some other way, and no verifier can ever match it.
		{"too short", rfcChallenge[:42], false},
		{"padded", rfcChallenge + "=", false},
		{"standard base64 alphabet", strings.Replace(rfcChallenge, "-", "+", 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidChallenge(tc.in); got != tc.want {
				t.Errorf("ValidChallenge(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
