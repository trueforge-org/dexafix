// Package mail is the worker for e-mail: it makes the connection to the SMTP server that the
// backend used to make, so the backend connects to nothing. "mail.send" sends one plain-text
// message. The SMTP settings and the message come with the job and are used for that job only; the
// message can hold a reset link, so the payload is never logged.
package mail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/trueforge-org/dexafix/pkg/mailer"
	"github.com/trueforge-org/dexafix/pkg/mailer/smtpsend"
	"github.com/trueforge-org/dexafix/pkg/shared/netguard"
	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
	"github.com/trueforge-org/dexafix/pkg/workers/runner"
)

// Provider is the provider's name: the first part of its kind.
const Provider = workerapi.ProviderMail

// KindSend is the kind this worker runs.
const KindSend = Provider + "." + workerapi.ActionSend

// Worker runs the mail kind of job.
type Worker struct{}

// New returns a worker.
func New() *Worker { return &Worker{} }

// Handlers returns the handler of each kind, for runner.Runner.Handle.
func (w *Worker) Handlers() map[string]runner.Handler {
	return map[string]runner.Handler{
		KindSend: func(ctx context.Context, job *runner.Job) (any, error) {
			var p workerapi.MailPayload
			if err := json.Unmarshal(job.Payload, &p); err != nil {
				return nil, runner.Permanent(errors.New("unreadable job"))
			}
			return nil, w.send(ctx, p)
		},
	}
}

// send delivers the message. The error text is stored with the job and shown to an administrator,
// so it holds the step that failed and never what the server answered.
func (w *Worker) send(ctx context.Context, p workerapi.MailPayload) error {
	allow := p.AllowPrivate
	s, err := smtpsend.New(mailer.Config{
		Host: p.Host, Port: p.Port, Username: p.Username, Password: p.Password, From: p.From, TLS: p.TLS,
		DialControl: netguard.Control(func() bool { return allow }),
	})
	if err != nil {
		return runner.Permanent(errors.New("the e-mail settings are not valid"))
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if err := s.Send(ctx, p.To, p.Subject, p.Body); err != nil {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, netguard.ErrBlocked):
			return runner.Permanent(errors.New("could not send the e-mail: " + workerapi.BlockedMessage))
		case err.Error() == "invalid recipient address" || strings.Contains(err.Error(), "line break"):
			return runner.Permanent(errors.New("could not send the e-mail: the message is not valid"))
		}
		step, _, _ := strings.Cut(err.Error(), ": ")
		return runner.Retryable(errors.New("could not send the e-mail ("+step+")"), 0)
	}
	return nil
}
