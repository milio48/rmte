package main

// ResizeWatcher observes terminal dimension changes while inside raw terminal mode
// and propagates resize control messages to the remote host.
type ResizeWatcher interface {
	Stop()
}
