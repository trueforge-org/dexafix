// Package mailer describes how the server sends e-mail: the SMTP configuration, its validation and
// the Sender interface. It makes no connection (the sender is in smtpsend, which only the mail
// worker uses), so the backend can check a configuration without being able to connect out.
package mailer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strings"
	"syscall"
)

// Sender sends one message to one recipient.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Transport security modes.
const (
	StartTLS = "starttls" // plain connection upgraded with STARTTLS, usually port 587
	TLS      = "tls"      // TLS from the first byte, usually port 465
	None     = "none"     // no encryption: only accepted for a server on this machine
)

// Config describes the SMTP server and the sender address.
type Config struct {
	Host     string
	Port     int
	Username string // empty: no authentication
	Password string
	From     string // address, optionally with a display name
	TLS      string // StartTLS (default), TLS or None
	// DialControl, when set, is the Control function of the connection's dialer: it sees the address
	// the host name resolved to, before the connection is made, and may refuse it.
	DialControl func(network, address string, c syscall.RawConn) error
}

// Check validates a configuration and returns it with the defaults filled in (port 587, STARTTLS).
// It makes no connection.
func Check(cfg Config) (Config, error) {
	if cfg.Host == "" {
		return cfg, errors.New("smtp host is empty")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.TLS == "" {
		cfg.TLS = StartTLS
	}
	switch cfg.TLS {
	case StartTLS, TLS:
	case None:
		if !isLoopback(cfg.Host) {
			return cfg, errors.New("smtp tls mode \"none\" is only allowed for a server on this machine (localhost): it would send the password and the reset links unencrypted")
		}
	default:
		return cfg, fmt.Errorf("unknown smtp tls mode %q: use starttls, tls or none", cfg.TLS)
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		return cfg, fmt.Errorf("smtp from address: %w", err)
	}
	return cfg, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
