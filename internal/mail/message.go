// Package mail composes transactional messages and delivers them through the durable outbox.
package mail

// Message is one outgoing e-mail. Key is the idempotency key forwarded to the relay.
type Message struct {
	Key     string
	ToEmail string
	ToName  string
	ReplyTo string
	Subject string
	HTML    string
	Text    string
}
