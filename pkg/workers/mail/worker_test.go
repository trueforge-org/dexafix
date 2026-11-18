package mail

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
	"github.com/trueforge-org/dexafix/pkg/workers/runner"
)

// fakeSMTP is an SMTP server on this machine that takes one message and keeps it.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	rcpt string
	data string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { c.Write([]byte(s + "\r\n")) }
	say("220 fake")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch verb := strings.ToUpper(strings.Fields(line + " ")[0]); verb {
		case "EHLO", "HELO":
			say("250 fake")
		case "MAIL":
			say("250 ok")
		case "RCPT":
			f.mu.Lock()
			f.rcpt = line
			f.mu.Unlock()
			say("250 ok")
		case "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func payload(f *fakeSMTP) workerapi.MailPayload {
	return workerapi.MailPayload{Host: "127.0.0.1", Port: f.port(), From: "Dexa <dexa@example.com>", TLS: "none",
		To: "alice@example.com", Subject: "Reset your password", Body: "https://dexa.example/reset?t=abc\n"}
}

func TestTheMailWorkerSendsTheMessage(t *testing.T) {
	f := newFakeSMTP(t)
	if err := New().send(context.Background(), payload(f)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.rcpt, "alice@example.com") || !strings.Contains(f.data, "Subject: Reset your password") || !strings.Contains(f.data, "https://dexa.example/reset?t=3Dabc") {
		t.Fatalf("rcpt %q data %q", f.rcpt, f.data)
	}
}

func TestTheMailWorkerSaysWhatKindOfFailureItIs(t *testing.T) {
	f := newFakeSMTP(t)
	for name, tc := range map[string]struct {
		mod       func(*workerapi.MailPayload)
		permanent bool
		want      string
	}{
		"settings that are not valid":   {func(p *workerapi.MailPayload) { p.Host = "" }, true, "settings are not valid"},
		"a recipient that is not valid": {func(p *workerapi.MailPayload) { p.To = "not an address" }, true, "message is not valid"},
		"a subject with a line break":   {func(p *workerapi.MailPayload) { p.Subject = "a\r\nBcc: x@example.com" }, true, "message is not valid"},
		"an address the guard blocks":   {func(p *workerapi.MailPayload) { p.Host, p.TLS = "169.254.169.254", "tls" }, true, workerapi.BlockedMessage},
		"a server that is not there":    {func(p *workerapi.MailPayload) { p.Port = 1 }, false, "could not send the e-mail (connect to"},
	} {
		t.Run(name, func(t *testing.T) {
			p := payload(f)
			tc.mod(&p)
			err := New().send(context.Background(), p)
			retry, _ := runner.IsRetryable(err)
			if err == nil || runner.IsPermanent(err) != tc.permanent || retry == tc.permanent || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New().send(ctx, payload(f)); err != context.Canceled {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestTheMailWorkerDoesNotRepeatWhatTheServerAnswered(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Write([]byte("554 secret-internal-page\r\n"))
			c.Close()
		}
	}()
	p := payload(&fakeSMTP{ln: ln})
	err = New().send(context.Background(), p)
	if err == nil || strings.Contains(err.Error(), "secret-internal-page") || !strings.Contains(err.Error(), "smtp greeting") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheHandlerDecodesItsJob(t *testing.T) {
	f := newFakeSMTP(t)
	h := New().Handlers()[KindSend]
	b, _ := json.Marshal(payload(f))
	if res, err := h(context.Background(), &runner.Job{Job: workerapi.Job{Payload: b}}); err != nil || res != nil {
		t.Fatalf("send: %v %v", res, err)
	}
	if _, err := h(context.Background(), &runner.Job{Job: workerapi.Job{Payload: []byte("{")}}); !runner.IsPermanent(err) {
		t.Fatalf("unreadable job: %v", err)
	}
}
