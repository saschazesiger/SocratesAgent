//go:build !linux && !windows

package accounts

// exchange is not offered here: the migration renames and then links
// (swap).
func exchange(a, b string) error { return errNoExchange }
