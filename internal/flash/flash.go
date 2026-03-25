package flash

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
)

const CookieName = "dsforms_flash"

// Set writes a signed flash cookie.
func Set(w http.ResponseWriter, secretKey, msgType, message string) {
	payload := base64.URLEncoding.EncodeToString([]byte(msgType + ":" + message))
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    payload + "." + sig,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Get reads and clears the flash cookie. Returns empty strings if missing/tampered.
func Get(r *http.Request, w http.ResponseWriter, secretKey string) (string, string) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return "", ""
	}

	// Split value into payload.signature
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return "", ""
	}
	payload, sig := parts[0], parts[1]

	// Verify HMAC
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return "", ""
	}

	// Decode payload
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return "", ""
	}

	// Split on first ":" for msgType:message
	idx := strings.Index(string(decoded), ":")
	if idx < 0 {
		return "", ""
	}

	// Clear the cookie only after successful validation
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	return string(decoded[:idx]), string(decoded[idx+1:])
}
