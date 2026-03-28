package ilink

// Hooks provides lifecycle callbacks for bot events.
// All fields are optional — nil hooks are simply not called.
type Hooks struct {
	// OnLogin is called after a successful login (QR scan or credential reuse).
	OnLogin func()

	// OnSessionExpired is called when the server returns -14 (session expired).
	// The poller will automatically pause and retry; this hook is informational.
	OnSessionExpired func()

	// OnSessionRecovered is called when the poller successfully resumes after a session expiry pause.
	OnSessionRecovered func()

	// OnBotStop is called when the polling loop exits (context cancelled or Stop called).
	OnBotStop func(err error)

	// OnError is called on non-fatal polling errors (network issues, transient API errors).
	OnError func(err error)

	// OnHandlerPanic is called when a message handler panics.
	// The recovered value and the originating message are provided.
	OnHandlerPanic func(recovered any, msg *Message)
}

func (h *Hooks) callOnLogin() {
	if h.OnLogin != nil {
		h.OnLogin()
	}
}

func (h *Hooks) callOnSessionExpired() {
	if h.OnSessionExpired != nil {
		h.OnSessionExpired()
	}
}

func (h *Hooks) callOnSessionRecovered() {
	if h.OnSessionRecovered != nil {
		h.OnSessionRecovered()
	}
}

func (h *Hooks) callOnBotStop(err error) {
	if h.OnBotStop != nil {
		h.OnBotStop(err)
	}
}

func (h *Hooks) callOnError(err error) {
	if h.OnError != nil {
		h.OnError(err)
	}
}

func (h *Hooks) callOnHandlerPanic(recovered any, msg *Message) {
	if h.OnHandlerPanic != nil {
		h.OnHandlerPanic(recovered, msg)
	}
}
