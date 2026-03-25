package flash

import (
	"net/http/httptest"
	"testing"
)

const testSecret = "test-secret-key-32-chars-long!!"

func TestSetWritesCookie(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	Set(w, testSecret, "success", "Password updated.")
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == CookieName {
			found = true
			if c.Value == "" {
				t.Error("cookie value is empty")
			}
		}
	}
	if !found {
		t.Fatal("flash cookie not set")
	}
}

func TestGetReadsAndClears(t *testing.T) {
	t.Parallel()
	w1 := httptest.NewRecorder()
	Set(w1, testSecret, "success", "Done!")
	cookies := w1.Result().Cookies()

	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	msgType, message := Get(req, w2, testSecret)
	if msgType != "success" {
		t.Errorf("msgType = %q, want %q", msgType, "success")
	}
	if message != "Done!" {
		t.Errorf("message = %q, want %q", message, "Done!")
	}
	clearCookies := w2.Result().Cookies()
	found := false
	for _, c := range clearCookies {
		if c.Name == CookieName && c.MaxAge == -1 {
			found = true
		}
	}
	if !found {
		t.Error("flash cookie not cleared after Get")
	}
}

func TestGetMissingCookie(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	msgType, message := Get(req, w, testSecret)
	if msgType != "" || message != "" {
		t.Errorf("expected empty strings, got %q %q", msgType, message)
	}
}

func TestGetTamperedCookie(t *testing.T) {
	t.Parallel()
	w1 := httptest.NewRecorder()
	Set(w1, testSecret, "error", "Bad thing happened")
	cookies := w1.Result().Cookies()

	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range cookies {
		c.Value = c.Value + "tampered"
		req.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	msgType, message := Get(req, w2, testSecret)
	if msgType != "" || message != "" {
		t.Errorf("expected empty strings for tampered, got %q %q", msgType, message)
	}
}

func TestFlashRoundTripTypes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		msgType string
		message string
	}{
		{"success", "Password updated."},
		{"error", "Something went wrong."},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.msgType, func(t *testing.T) {
			t.Parallel()
			w1 := httptest.NewRecorder()
			Set(w1, testSecret, tt.msgType, tt.message)
			req := httptest.NewRequest("GET", "/", nil)
			for _, c := range w1.Result().Cookies() {
				req.AddCookie(c)
			}
			w2 := httptest.NewRecorder()
			gotType, gotMsg := Get(req, w2, testSecret)
			if gotType != tt.msgType {
				t.Errorf("msgType = %q, want %q", gotType, tt.msgType)
			}
			if gotMsg != tt.message {
				t.Errorf("message = %q, want %q", gotMsg, tt.message)
			}
		})
	}
}
