//go:build !linux

package dbgate

import "fmt"

func withIdentity(uid int, fn func() error) error {
	if uid >= 0 {
		return fmt.Errorf("dbgate: filesystem isolation requires Linux")
	}
	return fn()
}
