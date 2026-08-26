// Package appsettings mirrors the deployment environment into PocketBase's
// settings record.
//
// Mail config lives in the app's own SQLite settings row, which means it would
// otherwise have to be typed into the dashboard by hand and would disappear
// with the volume. Driving it from the environment keeps it declarative in the
// k8s manifest and identical on every boot.
package appsettings

import (
	"os"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
)

// Apply copies APP_*/SMTP_* into the stored settings and saves them if
// anything actually moved. Only variables that are set are applied, so a
// value changed in the dashboard survives unless the deployment pins it.
func Apply(app core.App) error {
	s := app.Settings()
	changed := false

	str := func(dst *string, v string) {
		if v != "" && *dst != v {
			*dst = v
			changed = true
		}
	}

	str(&s.Meta.AppName, os.Getenv("APP_NAME"))
	// AppURL is what {APP_URL} resolves to in the verification email, so a
	// wrong value here produces links pointing at the wrong host.
	str(&s.Meta.AppURL, os.Getenv("APP_URL"))
	str(&s.Meta.SenderName, os.Getenv("SMTP_SENDER_NAME"))
	str(&s.Meta.SenderAddress, os.Getenv("SMTP_FROM"))

	if host := os.Getenv("SMTP_HOST"); host != "" {
		str(&s.SMTP.Host, host)
		str(&s.SMTP.Username, os.Getenv("SMTP_USERNAME"))
		str(&s.SMTP.Password, os.Getenv("SMTP_PASSWORD"))
		str(&s.SMTP.AuthMethod, os.Getenv("SMTP_AUTH_METHOD"))

		if port, err := strconv.Atoi(os.Getenv("SMTP_PORT")); err == nil && port > 0 && s.SMTP.Port != port {
			s.SMTP.Port = port
			changed = true
		}
		// SMTP_TLS asks for implicit TLS (port 465). Left false, PocketBase
		// issues STARTTLS instead, which is what Zoho wants on 587.
		if tls := os.Getenv("SMTP_TLS") == "true"; s.SMTP.TLS != tls {
			s.SMTP.TLS = tls
			changed = true
		}
		if !s.SMTP.Enabled {
			s.SMTP.Enabled = true
			changed = true
		}
	}

	if !changed {
		return nil
	}
	return app.Save(s)
}
