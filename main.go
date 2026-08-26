// Seekout: poll listing sources, match them against seekers, fire alerters.
// Single binary: PocketBase (DB, auth, REST, realtime, admin UI) + the
// polling pipeline + the web UI from embed.FS.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"

	"gitlab.mauza.net/mauza/seekout/internal/appsettings"
	"gitlab.mauza.net/mauza/seekout/internal/pipeline"
	_ "gitlab.mauza.net/mauza/seekout/internal/sources" // registers source types
	_ "gitlab.mauza.net/mauza/seekout/migrations"
)

//go:embed all:web
var webFS embed.FS

func main() {
	app := pocketbase.New()
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{Automigrate: true})

	// Mail settings come from the environment on every boot, after migrations
	// have run, so a fresh volume is fully configured without dashboard clicks.
	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		return appsettings.Apply(app)
	})

	if err := app.Cron().Add("poll_sources", "* * * * *", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		pipeline.PollDue(ctx, app)
	}); err != nil {
		log.Fatal(err)
	}

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// On-demand poll for one source (the web UI's "poll now" button).
		// The pipeline runs on a context detached from the request: a client
		// that disconnects mid-poll must not cancel alert delivery and leave
		// listings stored with their notifications half-sent.
		se.Router.POST("/api/seekout/sources/{id}/poll", func(e *core.RequestEvent) error {
			src, err := app.FindRecordById("sources", e.Request.PathValue("id"))
			if err != nil {
				return e.NotFoundError("source not found", err)
			}
			ctx, cancel := context.WithTimeout(context.WithoutCancel(e.Request.Context()), 10*time.Minute)
			defer cancel()
			stats, err := pipeline.PollSource(ctx, app, src)
			if err != nil {
				return e.BadRequestError("poll failed", err)
			}
			return e.JSON(http.StatusOK, stats)
		}).Bind(apis.RequireAuth())

		// Send a test notification through one alerter.
		se.Router.POST("/api/seekout/alerters/{id}/test", func(e *core.RequestEvent) error {
			if err := pipeline.TestAlerter(e.Request.Context(), app, e.Request.PathValue("id")); err != nil {
				return e.BadRequestError("test alert failed", err)
			}
			return e.JSON(http.StatusOK, map[string]bool{"ok": true})
		}).Bind(apis.RequireAuth())

		pub, err := fs.Sub(webFS, "web")
		if err != nil {
			return err
		}

		// The verification email links here. It needs an explicit route
		// because the static handler has no index fallback and the token
		// rides in the URL fragment.
		se.Router.GET("/verify", func(e *core.RequestEvent) error {
			return e.FileFS(pub, "verify.html")
		})
		// No index fallback: the UI has no client-side routes, and falling
		// back would answer unknown /api/... paths with the SPA instead of 404.
		se.Router.GET("/{path...}", apis.Static(pub, false))

		return se.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
