package mainbackend

import (
	"errors"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/trueforge-org/dexafix/pkg/shared/netguard"
	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
)

// The backend makes no outgoing connection of its own: the sign-in providers and the mail server are
// reached by workers (workerprovider_oidc.go, workerprovider_mail.go), and what the backend tells an
// administrator about a failure comes from their job.

// The two settings are read when a job is leased, so they apply without a restart. They are separate
// because they are separate risks: a provider is an address an administrator types once and the
// server then talks to on behalf of every visitor, the mail server is one they type for e-mail.
func (s *Backend) allowPrivateProviders() bool { return s.cfg().allowPrivateProviders }
func (s *Backend) allowPrivateMail() bool      { return s.cfg().allowPrivateMail }

// The names of the settings, as the Admin page shows them, for the messages that point to them.
const (
	settingLabelProviders = "Allow sign-in providers on private network addresses"
	settingLabelMail      = "Allow the mail server on a private network address"
)

// outboundFailure is what an administrator is told when a connection to a provider or the mail
// server failed: base ("Could not send the test e-mail"), a colon and the reason. The error itself
// is a worker's job error or a queue error; it goes to the log and not to the page. What is worth
// saying is that the guard refused the address, because that has a remedy: the setting named by
// settingLabel; and that no worker is running, which has one too.
func outboundFailure(err error, base, settingLabel string) string {
	log.Warn().Err(err).Str("action", base).Msg("outgoing connection failed")
	switch {
	case errors.Is(err, netguard.ErrBlocked), strings.Contains(err.Error(), workerapi.BlockedMessage):
		return base + ": that address is on a private or link-local network. If the server really is on your own network, turn on \"" + settingLabel + "\" under Network and retention."
	case errors.Is(err, errNoWorker):
		return base + ": no worker for that is running. Start one (see the workers page) and try again."
	case errors.Is(err, errKindPaused):
		return base + ": that kind of work is paused on the workers page."
	}
	return base + ": the server could not be reached or did not answer as expected. Check the address, the port and the encryption; the details are in the server log."
}
