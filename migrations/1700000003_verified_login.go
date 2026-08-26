package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Require a verified email address before an account can authenticate.
//
// PocketBase enforces this through the auth collection's authRule, which runs
// after the password check and before the token is handed out, so it covers
// every auth path (password, OAuth2, OTP) rather than just the login form.
//
// The verification email links to seekout's own /verify page instead of the
// PocketBase dashboard route the default template uses.
//
// Accounts that already exist predate the rule and are marked verified: they
// were created when signup was the only gate, and forcing them through
// verification would lock out whoever is already using the app.
func init() {
	migrations.Register(func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}

		if _, err := app.DB().NewQuery("UPDATE {{users}} SET verified = TRUE WHERE verified = FALSE").Execute(); err != nil {
			return err
		}

		users.AuthRule = types.Pointer("verified = true")
		users.VerificationTemplate = core.EmailTemplate{
			Subject: "Verify your email for {APP_NAME}",
			Body: `<p>Hello,</p>
<p>Confirm this address to finish setting up your {APP_NAME} account and start receiving listing alerts.</p>
<p>
  <a class="btn" href="{APP_URL}/verify#{TOKEN}" target="_blank" rel="noopener">Verify my email</a>
</p>
<p><i>The link is good for a few hours. If you didn't sign up, ignore this email.</i></p>
<p>&mdash; {APP_NAME}</p>`,
		}
		return app.Save(users)
	}, func(app core.App) error {
		users, err := app.FindCollectionByNameOrId("users")
		if err != nil {
			return err
		}
		users.AuthRule = types.Pointer("")
		return app.Save(users)
	}, "1700000003_verified_login.go")
}
