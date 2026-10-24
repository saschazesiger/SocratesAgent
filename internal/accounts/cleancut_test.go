//go:build !windows

package accounts

import (
	"encoding/json"
	"testing"
)

// A prepared chat the clean cut made is given the account its profile
// became, loses the migration's own option either way, and keeps an account
// somebody chose on its panel before the migration was finished.
func TestAPreparedChatIsBoundToItsProfilesAccount(t *testing.T) {
	for _, c := range []struct{ options, account, want string }{
		{`{"pre_rebuild_profile":"p-work","ultracode":true}`, "a-work", `{"account":"a-work","ultracode":true}`},
		{`{"pre_rebuild_profile":"p-work","account":"a-mine"}`, "a-work", `{"account":"a-mine"}`},
		{`{"pre_rebuild_profile":""}`, "", `{}`},
	} {
		if got := string(bound(json.RawMessage(c.options), c.account)); got != c.want {
			t.Errorf("%s bound to %q = %s, want %s", c.options, c.account, got, c.want)
		}
	}
}
