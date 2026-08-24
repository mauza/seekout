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

	"gitlab.mauza.net/mauza/seekout/internal/pipeline"
	_ "gitlab.mauza.net/mauza/seekout/internal/sources" // registers source types
	_ "gitlab.mauza.net/mauza/seekout/migrations"
)

//go:embed all:web
var webFS embed.FS

func main() {
	app := pocketbase.New()
	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{Automigrate: true})

	if err := app.Cron().Add("poll_sources", "* * * * *", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		pipeline.PollDue(ctx, app)
	}); err != nil {
		log.Fatal(err)
	}

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Synchronous on-demand poll for one source (used by the web UI's
		// "poll now" button and for debugging).
		se.Router.POST("/api/seekout/sources/{id}/poll", func(e *core.RequestEvent) error {
			src, err := app.FindRecordById("sources", e.Request.PathValue("id"))
			if err != nil {
				return e.NotFoundError("source not found", err)
			}
			stats, err := pipeline.PollSource(e.Request.Context(), app, src)
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
		se.Router.GET("/{path...}", apis.Static(pub, true))

		return se.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
