package mailer

import (
	"strings"
	"testing"
)

func TestCheckValidatesConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		cfg     Config
		wantErr string // a part of the error message; "" means accepted
	}{
		{"empty host", Config{From: "dexa@example.com"}, "host is empty"},
		{"bad from address", Config{Host: "smtp.example.com", From: "not an address"}, "from address"},
		{"empty from address", Config{Host: "smtp.example.com"}, "from address"},
		{"tls none to a remote host", Config{Host: "smtp.example.com", From: "dexa@example.com", TLS: None}, "only allowed"},
		{"tls none to localhost", Config{Host: "localhost", From: "dexa@example.com", TLS: None}, ""},
		{"tls none to 127.0.0.1", Config{Host: "127.0.0.1", From: "dexa@example.com", TLS: None}, ""},
		{"tls none to ::1", Config{Host: "::1", From: "dexa@example.com", TLS: None}, ""},
		{"unknown tls mode", Config{Host: "smtp.example.com", From: "dexa@example.com", TLS: "plain"}, "unknown smtp tls mode"},
		{"tls mode tls", Config{Host: "smtp.example.com", From: "dexa@example.com", TLS: TLS}, ""},
		{"display name in from", Config{Host: "smtp.example.com", From: "Dexa <dexa@example.com>"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Check(tt.cfg)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("accepted, want an error with %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheckFillsInTheDefaults(t *testing.T) {
	cfg, err := Check(Config{Host: "smtp.example.com", From: "dexa@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 587 || cfg.TLS != StartTLS {
		t.Fatalf("defaults: port %d, tls %q", cfg.Port, cfg.TLS)
	}
}
