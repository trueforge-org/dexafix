package mainbackend

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
)

// mailSink is an SMTP server on this machine that keeps the messages it was given.
type mailSink struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []string
}

func newMailSink(t *testing.T) *mailSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &mailSink{ln: ln}
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

func (f *mailSink) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { c.Write([]byte(s + "\r\n")) }
	say("220 sink")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch strings.ToUpper(strings.Fields(line + " ")[0]) {
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
			f.msgs = append(f.msgs, b.String())
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

func (f *mailSink) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *mailSink) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.msgs...)
}

// useSMTP saves e-mail settings that point at host:port and puts them in force.
func (g *rig) useSMTP(host string, port int) {
	g.t.Helper()
	ctx := context.Background()
	for k, v := range map[string]string{settingSMTPHost: host, settingSMTPPort: strconv.Itoa(port), settingSMTPFrom: "Dexa <dexa@example.com>",
		settingSMTPTLS: "none", settingSMTPPassword: "the-smtp-password"} {
		if err := g.st.PutSetting(ctx, k, v); err != nil {
			g.t.Fatal(err)
		}
	}
	if err := g.b.reloadSettings(ctx); err != nil {
		g.t.Fatal(err)
	}
}

func TestAResetMailIsQueuedAsAJobAndSentByTheMailWorker(t *testing.T) {
	g := newProviderRig(t)
	sink := newMailSink(t)
	g.useSMTP("127.0.0.1", sink.port())
	uid, _ := g.signUp("alice")
	u, err := g.st.GetUser(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	u.Email = "alice@example.com"
	if !g.b.sendResetMail(u, "https://dexa.example.com/password/reset?t=the-token", time.Hour) {
		t.Fatal("the mail was not queued")
	}
	waitFor(t, "the worker sending the mail", func() bool { return len(sink.all()) == 1 })
	if m := sink.all()[0]; !strings.Contains(m, "To: alice@example.com") || !strings.Contains(m, "the-token") {
		t.Fatalf("message:\n%s", m)
	}
	// The link is a credential: it is not kept with the job once the job ended.
	var jobs []models.Job
	waitFor(t, "the job ending", func() bool {
		jobs, _ = g.st.ListJobs(context.Background(), models.JobFilter{Kinds: []string{kindMailSend}, States: []string{models.JobSucceeded}})
		return len(jobs) == 1
	})
	if j, err := g.st.GetJob(context.Background(), jobs[0].PublicID); err != nil || j.Payload != "" || j.Result != "" {
		t.Fatalf("the finished job keeps %q / %q (%v)", j.Payload, j.Result, err)
	}
}

func TestTheMailPasswordIsInTheLeaseAndNotInTheJob(t *testing.T) {
	g := newRig(t)
	g.useSMTP("127.0.0.1", 2525)
	stored := `{"to":"alice@example.com","subject":"s","body":"b"}`
	if strings.Contains(stored, "the-smtp-password") {
		t.Fatal("a mail job has a field for the password")
	}
	lease, err := leaseMailSend(context.Background(), g.b, models.Job{Payload: stored})
	if err != nil {
		t.Fatal(err)
	}
	p := lease.(workerapi.MailPayload)
	if p.Password != "the-smtp-password" || p.Host != "127.0.0.1" || p.Port != 2525 || p.To != "alice@example.com" || p.AllowPrivate {
		t.Fatalf("lease %+v", p)
	}
	g.b.allowPrivateMail() // the setting is read when the job is leased
	l := *g.b.cfg()
	l.allowPrivateMail = true
	g.b.swapSettings(&l)
	if lease, _ = leaseMailSend(context.Background(), g.b, models.Job{Payload: stored}); !lease.(workerapi.MailPayload).AllowPrivate {
		t.Fatal("the private-network setting did not reach the worker")
	}
	for name, job := range map[string]models.Job{"an unreadable job": {Payload: "{"}} {
		if _, err := leaseMailSend(context.Background(), g.b, job); err == nil {
			t.Fatalf("%s was leased", name)
		}
	}
	if err := g.st.DeleteSetting(context.Background(), settingSMTPHost); err != nil {
		t.Fatal(err)
	}
	if _, err := leaseMailSend(context.Background(), g.b, models.Job{Payload: stored}); err == nil {
		t.Fatal("a job was leased although e-mail is off")
	}
}

func TestAResetMailWithoutAMailWorkerIsNotSent(t *testing.T) {
	g := newRig(t) // no worker has ever asked for work
	g.useSMTP("127.0.0.1", 2525)
	if g.b.sendResetMail(models.User{ID: 1, Username: "alice", Email: "alice@example.com"}, "https://dexa.example.com/x", time.Hour) {
		t.Fatal("a mail nobody can send was reported as sent")
	}
	if jobs, _ := g.st.ListJobs(context.Background(), models.JobFilter{Kinds: []string{kindMailSend}}); len(jobs) != 0 {
		t.Fatalf("%d jobs were queued", len(jobs))
	}
}

func TestTheTestMailWaitsForTheWorkerAndReportsHowItWent(t *testing.T) {
	g := newProviderRig(t)
	sink := newMailSink(t)
	g.useSMTP("127.0.0.1", sink.port())
	m := &jobMailer{s: g.b}
	if err := m.waiting().Send(context.Background(), "root@example.com", "Test", "Hello\n"); err != nil {
		t.Fatal(err)
	}
	if len(sink.all()) != 1 {
		t.Fatalf("the worker had not sent it when the call returned: %d", len(sink.all()))
	}
	// A server the guard refuses ends the job with the text the administrator is pointed to.
	g.useSMTP("169.254.169.254", 25)
	if err := g.st.PutSetting(context.Background(), settingSMTPTLS, "tls"); err != nil { // "none" is only for this machine
		t.Fatal(err)
	}
	if err := g.b.reloadSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := m.waiting().Send(context.Background(), "root@example.com", "Test", "Hello\n")
	if err == nil || !strings.Contains(err.Error(), workerapi.BlockedMessage) {
		t.Fatalf("a blocked mail server: %v", err)
	}
	if msg := outboundFailure(err, "Could not send the test e-mail", settingLabelMail); !strings.Contains(msg, settingLabelMail) {
		t.Fatalf("message %q", msg)
	}
}

func TestSendingMailSaysWhyItCouldNotQueueTheMessageOrGetItSent(t *testing.T) {
	base := newRig(t)
	faults := &jobFaults{Store: base.st}
	g := newRigOn(t, faults, base.st)
	g.useSMTP("127.0.0.1", 2525)
	ctx := context.Background()
	m := &jobMailer{s: g.b}
	send := func(m *jobMailer) error { return m.Send(ctx, "alice@example.com", "Subject", "Body\n") }

	faults.failOn("ListWorkers")
	if err := send(m); !errors.Is(err, errFlaky) {
		t.Fatalf("a store that fails: %v", err)
	}
	faults.failOn()
	for name, mm := range map[string]*jobMailer{"queued": m, "waiting": m.waiting()} {
		if err := send(mm); !errors.Is(err, errNoWorker) {
			t.Fatalf("%s, with no worker: %v", name, err)
		}
	}
	// A worker is listed but never leases anything: the page stops waiting and the job is dropped.
	if err := g.st.TouchWorker(ctx, "idle", []string{kindMailSend}, "v", time.Now()); err != nil {
		t.Fatal(err)
	}
	old := mailWait
	mailWait = 150 * time.Millisecond
	t.Cleanup(func() { mailWait = old })
	if err := send(m.waiting()); err == nil || !strings.Contains(err.Error(), "did not answer in time") {
		t.Fatalf("a worker that never comes: %v", err)
	}
	if jobs, _ := g.st.ListJobs(ctx, models.JobFilter{Kinds: []string{kindMailSend}, States: []string{models.JobCanceled}}); len(jobs) != 1 {
		t.Fatalf("%d canceled jobs", len(jobs))
	}
	// An administrator who closes the page stops the wait.
	mailWait = time.Minute
	gone, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := m.waiting().Send(gone, "alice@example.com", "Subject", "Body\n"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a page that was closed: %v", err)
	}
	// The settings are read when the job is leased.
	faults.failOn("GetSetting")
	if _, err := leaseMailSend(ctx, g.b, models.Job{Payload: `{"to":"alice@example.com"}`}); !errors.Is(err, errFlaky) {
		t.Fatalf("settings that cannot be read: %v", err)
	}
}
