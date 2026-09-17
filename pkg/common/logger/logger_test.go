package logger

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/lmittmann/tint"
)

func TestRedactingHandlerRedactsURLsAndCredentials(t *testing.T) {
	var output bytes.Buffer
	log := testLogger(&output).With("provider_url", "https://user:pass@rpc.example/api/key?api_key=abc")
	log.Error("request failed: Get https://rpc.example/v1?token=top-secret", "error", errors.New("authorization=Bearer-secret https://rpc.example/?key=value"))

	got := output.String()
	for _, secret := range []string{"user:pass", "top-secret", "Bearer-secret", "key=value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("log contains secret %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "https://rpc.example") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("log does not show redaction markers: %s", got)
	}
}

func TestRedactURLRetainsOnlyEndpointOrigin(t *testing.T) {
	got := redactURL("https://user:password@rpc.example:443/v1/project-key?api_key=secret")
	if got != "https://rpc.example:443" {
		t.Fatalf("redactURL() = %q", got)
	}
}

func TestRedactingHandlerRedactsNestedSensitiveAttributes(t *testing.T) {
	var output bytes.Buffer
	testLogger(&output).Info("connected", slog.Group("connection", slog.String("token", "secret-value")))
	if strings.Contains(output.String(), "secret-value") {
		t.Fatalf("nested sensitive value was logged: %s", output.String())
	}
}

func testLogger(output *bytes.Buffer) *slog.Logger {
	return slog.New(tint.NewHandler(output, &tint.Options{
		NoColor:     true,
		ReplaceAttr: redactAttr,
	}))
}
