//go:build windows

package runner

import (
	"context"
	"fmt"
	"os"
)

// EnsureSecretsReadable grants the runner's service account read access to srm's
// secrets files (secrets.age + secrets.pass) via icacls, so the ephemeral supervisor
// - which runs as that low-privilege account (NETWORK SERVICE by default), NOT as
// SYSTEM - can open the age store and decrypt the App key. Without it a headless
// key import leaves the store unreadable by the very process that needs it, and the
// lane fails auth every cycle. The grantee is resolved through icaclsGrantee so the
// built-in service accounts go in by well-known SID (locale-safe; a localized display
// name can fail to resolve with error 1332).
//
// A read grant (not Modify): the runner only ever decrypts these, never rewrites
// them. Absent paths are skipped, so calling it before a store exists is harmless.
func EnsureSecretsReadable(ctx context.Context, account string, paths ...string) error {
	grantee := icaclsGrantee(account)
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue // no store/passphrase file yet - nothing to grant
		}
		grant := fmt.Sprintf(`%s:(R)`, grantee)
		if err := run(ctx, "icacls", p, "/grant", grant, "/C", "/Q"); err != nil {
			return fmt.Errorf("grant read on %s to %s: %w", p, account, err)
		}
	}
	return nil
}
