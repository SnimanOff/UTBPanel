//go:build !linux

package botdir

// Secure is needed only where bots run under their own uid (Linux,
// the core as root).
func Secure(string, int) error { return nil }
