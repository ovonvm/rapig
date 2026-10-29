package fetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/a.zip",
		"https://example.com:8443/x",
	} {
		if _, err := ValidateURL(raw); err != nil {
			t.Fatalf("ValidateURL(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{
		"",
		"ftp://example.com/x",
		"file:///etc/passwd",
		"http://user:pw@example.com/x",
		"https://",
		"http://" + strings.Repeat("a", 3000),
	} {
		if _, err := ValidateURL(raw); !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("ValidateURL(%q) err=%v, want ErrInvalidURL", raw, err)
		}
	}
}

func TestOpenRejectsPrivateTargets(t *testing.T) {
	c := New(false)
	for _, raw := range []string{
		"http://127.0.0.1:9/x",
		"http://10.1.2.3/x",
		"http://169.254.169.254/latest/meta-data",
		"http://192.168.1.1/x",
		"http://[::1]/x",
	} {
		if _, err := c.Open(context.Background(), raw); !errors.Is(err, ErrBlocked) {
			t.Fatalf("Open(%q) err=%v, want ErrBlocked", raw, err)
		}
	}
}

func TestOpenAllowsLoopbackWhenEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "payload")
	}))
	defer srv.Close()

	resp, err := New(true).Open(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q", body)
	}
}

func TestOpenRejectsNonHTTP(t *testing.T) {
	if _, err := New(true).Open(context.Background(), "ftp://example.com/x"); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("err = %v, want ErrInvalidURL", err)
	}
}
