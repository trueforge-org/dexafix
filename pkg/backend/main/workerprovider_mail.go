package mainbackend

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
)

// E-mail: the SMTP server is reached by a worker (pkg/workers/mail), so the backend connects to
// nothing. The backend keeps the settings and the password (Admin page, encrypted) and puts them in
// the lease of a job; the job itself holds only the message, which can hold a reset link, and is
// cleared when the job ends.
//
// jobMailer is the mailer.Sender the rest of the backend uses: sending a message queues a job.

const (
	providerMail = workerapi.ProviderMail

	// mailExpiry is how long a queued message may wait for a worker before it is dropped: a reset
	// link is good for a limited time, and a message that arrives hours late helps nobody.
	mailExpiry = 15 * time.Minute
	// mailWait is how long the Admin page waits for the test e-mail; the worker's own limit is 40 s.
	mailWait = 50 * time.Second
)

var kindMailSend = workerapi.Kind(providerMail, workerapi.ActionSend)

// mailJob is the parameters of a mail.send job.
type mailJob struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func mailProviderSpec() workerProvider {
	return workerProvider{
		Name: providerMail, Label: "E-mail",
		Kinds: []kindSpec{{
			Kind: kindMailSend, Label: "E-mail",
			Defaults: kindDefaults{MaxRunning: 4, MaxAttempts: 3, RetryBase: 30 * time.Second, Timeout: time.Minute},
			Lease:    leaseMailSend,
			// Nothing to keep: a message that went out leaves no trace but the audit log.
			Apply: func(context.Context, *Backend, models.Job, json.RawMessage) (string, error) { return "", nil },
		}},
	}
}

func leaseMailSend(ctx context.Context, s *Backend, j models.Job) (any, error) {
	var in mailJob
	if err := json.Unmarshal([]byte(j.Payload), &in); err != nil {
		return nil, permanent(errors.New("unreadable job"))
	}
	cfg, _, err := s.loadSMTP(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.Host == "" {
		return nil, permanent(errors.New("e-mail is off"))
	}
	return workerapi.MailPayload{
		Host: cfg.Host, Port: cfg.Port, Username: cfg.Username, Password: cfg.Password, From: cfg.From, TLS: cfg.TLS,
		AllowPrivate: s.allowPrivateMail(), To: in.To, Subject: in.Subject, Body: in.Body,
	}, nil
}

// jobMailer sends by queuing a job. By default it does not wait: the message goes out when a worker
// gets to it. A waiting one (the Admin page's test e-mail) reports how the job ended.
type jobMailer struct {
	s    *Backend
	wait bool
}

func (m *jobMailer) waiting() *jobMailer { return &jobMailer{s: m.s, wait: true} }

// Send queues the message. Without a worker that runs the kind it refuses at once (errNoWorker), so
// a person or an administrator is told instead of a message waiting for nobody.
func (m *jobMailer) Send(ctx context.Context, to, subject, body string) error {
	e := enqueueSpec{
		Kind: kindMailSend, Origin: models.JobOriginUser, Interactive: m.wait, Expires: mailExpiry, SingleAttempt: m.wait,
		Payload: mailJob{To: to, Subject: subject, Body: body},
	}
	if !m.wait {
		online, err := m.s.workerOnline(ctx, kindMailSend)
		if err != nil {
			return err
		}
		if !online {
			return errNoWorker
		}
		_, _, err = m.s.enqueueJob(ctx, e)
		return err
	}
	j, _, err := m.s.enqueueJob(ctx, e)
	if err != nil {
		return err
	}
	if j, err = m.s.waitJob(ctx, j.PublicID, mailWait); err != nil {
		return err
	}
	switch {
	case models.JobActive(j.State):
		if _, cerr := m.s.store.CancelJob(context.WithoutCancel(ctx), j.PublicID, 0, time.Now()); cerr != nil {
			return cerr
		}
		return errors.New("the worker did not answer in time")
	case j.State != models.JobSucceeded:
		return jobFailed(j)
	}
	return nil
}
