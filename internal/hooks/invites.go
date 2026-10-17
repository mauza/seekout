package hooks

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// inviteAlphabet leaves out look-alikes (0/o, 1/l/i) since codes get read
// out and retyped.
const inviteAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// bindInvites makes signup invite-only. The invite code rides in the signup
// body as "invite"; it is not a users field.
func bindInvites(app core.App) {
	// Codes are always server-generated and stamped with their creator.
	app.OnRecordCreateRequest("invites").BindFunc(func(e *core.RecordRequestEvent) error {
		e.Record.Set("code", security.RandomStringWithAlphabet(10, inviteAlphabet))
		e.Record.Set("used_by", "")
		e.Record.Set("used_at", "")
		if e.Auth != nil && e.Auth.Collection().Name == "users" {
			e.Record.Set("created_by", e.Auth.Id)
		}
		return e.Next()
	})

	// Claiming the code and creating the user share one transaction: a code
	// is spent only if the account is really created, and two signups racing
	// for one code can't both win. Superusers (dashboard, prod-test) create
	// accounts without one.
	app.OnRecordCreateRequest("users").BindFunc(func(e *core.RecordRequestEvent) error {
		if e.HasSuperuserAuth() {
			return e.Next()
		}
		info, err := e.RequestInfo()
		if err != nil {
			return err
		}
		code, _ := info.Body["invite"].(string)
		code = strings.ToLower(strings.TrimSpace(code))
		if code == "" {
			return fieldErr("invite", "an invite code is required to sign up")
		}
		return e.App.RunInTransaction(func(txApp core.App) error {
			invite, err := txApp.FindFirstRecordByFilter("invites",
				"code = {:c} && used_by = ''", dbx.Params{"c": code})
			if errors.Is(err, sql.ErrNoRows) {
				return fieldErr("invite", "this invite code is not valid or has already been used")
			}
			if err != nil {
				return err
			}
			e.App = txApp
			if err := e.Next(); err != nil {
				return err
			}
			invite.Set("used_by", e.Record.Id)
			invite.Set("used_at", time.Now().UTC().Format("2006-01-02 15:04:05.000Z"))
			return txApp.Save(invite)
		})
	})
}
