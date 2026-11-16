package migrate

// WithFail makes h's run fail at the step fail says, before that step changes anything: a crash there.
func WithFail(h Host, fail func(step string) error) Host {
	h.fail = fail
	return h
}
