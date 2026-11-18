package workerapi

// E-mail: the connection to the SMTP server, made by a worker so the backend connects to nothing.
//
//	mail.send  MailPayload - send one plain-text message to one recipient.
//
// The lease carries the SMTP settings (with the password) and the message, and the payload is
// cleared when the job ends: a reset link is a credential.

// ActionSend is the action of the mail provider.
const ActionSend = "send"

// MailPayload is the payload of mail.send.
type MailPayload struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
	TLS      string `json:"tls,omitempty"` // starttls, tls or none
	// AllowPrivate is the administrator's setting for the mail server on a private network address.
	AllowPrivate bool `json:"allow_private,omitempty"`

	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}
