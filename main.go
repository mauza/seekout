// Seekout: poll listing sources, match them against seekers, fire alerters.
// Single binary: PocketBase (DB, auth, REST, realtime, admin UI) + the
// polling pipeline + the web UI from embed.FS.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	"github.com/spf13/cobra"

	"github.com/mauza/seekout/internal/alerters"
	"github.com/mauza/seekout/internal/appsettings"
	"github.com/mauza/seekout/internal/hooks"
	"github.com/mauza/seekout/internal/listing"
	"github.com/mauza/seekout/internal/match"
	"github.com/mauza/seekout/internal/mcpserver"
	"github.com/mauza/seekout/internal/netguard"
	"github.com/mauza/seekout/internal/pipeline"
	"github.com/mauza/seekout/internal/pricing"
	"github.com/mauza/seekout/internal/sources"
	_ "github.com/mauza/seekout/migrations"
)

//go:embed all:web
var webFS embed.FS

// version is reported to MCP clients; keep it in step with the image tag.
const version = "0.10.0"

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

	// Price tracking is on when an LLM is configured. Result pages fetched
	// during research come from the web, so they go through netguard.
	pipeline.Pricing = pricing.FromEnv(netguard.Client)

	app.RootCmd.AddCommand(mcpTokenCmd(app), adminCmd(app), pricingCmd(app))

	// Record rules the collection API rules can't express (seeker conditions,
	// alerter ownership, per-type config); they cover the REST API, MCP and
	// server code alike.
	hooks.Bind(app)

	if err := app.Cron().Add("poll_sources", "* * * * *", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		pipeline.PollDue(ctx, app)
	}); err != nil {
		log.Fatal(err)
	}

	if pipeline.Pricing != nil {
		// Listings whose classification failed get another go each hour.
		if err := app.Cron().Add("pricing_retry", "17 * * * *", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			if _, err := pipeline.RetryPricing(ctx, app); err != nil {
				app.Logger().Error("pricing retry", "error", err)
			}
		}); err != nil {
			log.Fatal(err)
		}
		// Daily; each item is researched once per SEEKOUT_RESEARCH_EVERY_DAYS.
		if err := app.Cron().Add("pricing_research", "0 11 * * *", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			n, err := pipeline.Pricing.ResearchDue(ctx, app)
			if err != nil {
				app.Logger().Error("pricing research", "items", n, "error", err)
			}
			if _, err := pipeline.Reprice(app, 7); err != nil {
				app.Logger().Error("pricing reprice", "error", err)
			}
		}); err != nil {
			log.Fatal(err)
		}
	}

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Migrations have run by now. Registered source/alerter types and
		// listing kinds join their select fields here, so adding one needs
		// no migration.
		if err := hooks.SyncSelects(app); err != nil {
			return err
		}

		// What the web UI builds its forms from: every source and alerter
		// type with its config fields, and the listing kinds.
		se.Router.GET("/api/seekout/plugins", func(e *core.RequestEvent) error {
			return e.JSON(http.StatusOK, map[string]any{
				"sources":  sources.Infos(),
				"alerters": alerters.Infos(),
				"kinds":    listing.Kinds,
			})
		}).Bind(apis.RequireAuth())

		// Kubernetes probes hit /api/health every few seconds; logging each
		// success buried everything else in the request log (and wrote to
		// auxiliary.db constantly). Failures are still logged.
		se.Router.BindFunc(func(e *core.RequestEvent) error {
			if e.Request.URL.Path == "/api/health" {
				return apis.SkipSuccessActivityLog().Func(e)
			}
			return e.Next()
		})

		// Live preview for the seeker editor: which stored listings a
		// seeker with these settings would have matched.
		se.Router.POST("/api/seekout/preview", func(e *core.RequestEvent) error {
			var in previewRequest
			if err := e.BindBody(&in); err != nil {
				return e.BadRequestError("invalid body", err)
			}
			out, err := preview(app, in)
			if err != nil {
				return e.BadRequestError(err.Error(), nil)
			}
			return e.JSON(http.StatusOK, out)
		}).Bind(apis.RequireAuth())

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
			if errors.Is(err, pipeline.ErrBusy) {
				return e.Error(http.StatusConflict, "This source is already being polled; try again in a minute.", nil)
			}
			if err != nil {
				return e.BadRequestError("poll failed", err)
			}
			return e.JSON(http.StatusOK, stats)
		}).Bind(apis.RequireAuth())

		// Price tracking: items with their current estimate, and one
		// item's observations and research. Shared data, like listings.
		se.Router.GET("/api/seekout/items", func(e *core.RequestEvent) error {
			items, err := pricing.Items(app, 90, 30*24*time.Hour)
			if err != nil {
				return e.InternalServerError("items", err)
			}
			return e.JSON(http.StatusOK, map[string]any{"items": items, "enabled": pipeline.Pricing != nil})
		}).Bind(apis.RequireAuth())
		se.Router.GET("/api/seekout/items/{id}", func(e *core.RequestEvent) error {
			d, err := pricing.Item(app, e.Request.PathValue("id"), 90, 30*24*time.Hour)
			if err != nil {
				return e.NotFoundError("item not found", err)
			}
			return e.JSON(http.StatusOK, d)
		}).Bind(apis.RequireAuth())

		// Send a test notification through one of the caller's alerters.
		se.Router.POST("/api/seekout/alerters/{id}/test", func(e *core.RequestEvent) error {
			if err := pipeline.TestAlerter(e.Request.Context(), app, e.Request.PathValue("id"), e.Auth.Id); err != nil {
				return e.BadRequestError("test alert failed", err)
			}
			return e.JSON(http.StatusOK, map[string]bool{"ok": true})
		}).Bind(apis.RequireAuth())

		// MCP for agents (Hermes). Auth is its own bearer check, so it
		// answers MCP clients with a 401 + WWW-Authenticate they understand.
		// Explicit methods: a method-less "/mcp" conflicts with the
		// "GET /{path...}" static route below and panics the router.
		mcpHandler := apis.WrapStdHandler(mcpserver.Handler(app, version))
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			se.Router.Route(method, "/mcp", mcpHandler)
		}

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

type previewRequest struct {
	Kind       string            `json:"kind"`
	Sources    []string          `json:"sources"`
	Conditions []match.Condition `json:"conditions"`
	Days       int               `json:"days"`  // look back this far; default 30
	Limit      int               `json:"limit"` // examples to return; default 6
}

// preview runs a seeker's settings over recently stored listings. Listings
// are shared, so this exposes nothing the caller can't already read.
func preview(app core.App, in previewRequest) (map[string]any, error) {
	if err := match.Validate(in.Conditions); err != nil {
		return nil, err
	}
	days := in.Days
	if days <= 0 || days > 365 {
		days = 30
	}
	limit := in.Limit
	if limit <= 0 || limit > 50 {
		limit = 6
	}
	since := time.Now().AddDate(0, 0, -days)
	res, err := pipeline.Search(app, pipeline.Query{
		Kind: in.Kind, Sources: in.Sources, Since: since, Conditions: in.Conditions,
	}, limit)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(res.Hits))
	for _, h := range res.Hits {
		item := h.Record.PublicExport()
		if len(h.Result.Parts) > 0 {
			item["score"], item["parts"] = h.Result.Score, h.Result.Parts
		}
		items = append(items, item)
	}
	return map[string]any{
		"days":     days,
		"scanned":  res.Scanned,
		"capped":   res.Scanned >= pipeline.SearchScan,
		"matched":  res.Matched,
		"listings": items,
	}, nil
}

// adminCmd grants or revokes admin (managing the shared sources and
// invites). It is a CLI command because no API path may set the flag.
func adminCmd(app *pocketbase.PocketBase) *cobra.Command {
	var revoke bool
	cmd := &cobra.Command{
		Use:          "admin <email>",
		Short:        "Make a user an admin (or --revoke)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Bootstrap(); err != nil {
				return err
			}
			if err := app.RunAllMigrations(); err != nil {
				return err
			}
			user, err := app.FindAuthRecordByEmail("users", args[0])
			if err != nil {
				return fmt.Errorf("user %q: %w", args[0], err)
			}
			user.Set("admin", !revoke)
			if err := app.SaveNoValidate(user); err != nil {
				return err
			}
			fmt.Printf("%s admin=%v\n", args[0], !revoke)
			return nil
		},
	}
	cmd.Flags().BoolVar(&revoke, "revoke", false, "remove admin instead")
	return cmd
}

// mcpTokenCmd mints a long-lived, non-refreshable auth token for a user, for
// MCP clients that can only hold a static bearer token. Changing the user's
// password invalidates every token issued this way.
func mcpTokenCmd(app *pocketbase.PocketBase) *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "mcp-token <email>",
		Short: "Print a static MCP bearer token for a user",
		Args:  cobra.ExactArgs(1),
		// Runtime errors (unknown email) are not usage errors.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Bootstrap(); err != nil {
				return err
			}
			user, err := app.FindAuthRecordByEmail("users", args[0])
			if err != nil {
				return fmt.Errorf("user %q: %w", args[0], err)
			}
			if !user.Verified() {
				return fmt.Errorf("user %q has not verified their email", args[0])
			}
			token, err := user.NewStaticAuthToken(time.Duration(days) * 24 * time.Hour)
			if err != nil {
				return err
			}
			fmt.Println(token)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 365, "token lifetime in days")
	return cmd
}

// pricingCmd runs price tracking by hand: backfill classifies stored
// listings the seekers want and reprices them; research runs web research
// now (for one item, or every due item); reprice recomputes deals only.
func pricingCmd(app *pocketbase.PocketBase) *cobra.Command {
	cmd := &cobra.Command{Use: "pricing", Short: "Price tracking: backfill, research, reprice"}
	boot := func() error {
		if pipeline.Pricing == nil {
			return errors.New("pricing is off: set SEEKOUT_LLM_URL and SEEKOUT_LLM_KEY")
		}
		if err := app.Bootstrap(); err != nil {
			return err
		}
		return app.RunAllMigrations()
	}
	var days int
	var redo []string
	backfill := &cobra.Command{
		Use: "backfill", Short: "Classify and price stored listings from the last --days days", SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := boot(); err != nil {
				return err
			}
			st, err := pipeline.Backfill(cmd.Context(), app, days, redo, func(s string) { fmt.Println(s) })
			fmt.Printf("scanned %d, filed %d, failed %d, not wanted %d, repriced %d\n", st.Scanned, st.Filed, st.Failed, st.NotWanted, st.Repriced)
			return err
		},
	}
	backfill.Flags().IntVar(&days, "days", 30, "look back this many days")
	backfill.Flags().StringSliceVar(&redo, "redo", nil, "classify listings with these statuses again from scratch (ok,bundle,off_target,unclear,failed)")

	var item string
	research := &cobra.Command{
		Use: "research", Short: "Research due items now (or --item <id>, due or not)", SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := boot(); err != nil {
				return err
			}
			if item == "" {
				n, err := pipeline.Pricing.ResearchDue(cmd.Context(), app)
				fmt.Printf("researched %d item(s)\n", n)
				return err
			}
			it, err := app.FindRecordById("items", item)
			if err != nil {
				return fmt.Errorf("item %q: %w", item, err)
			}
			ref, err := pipeline.Pricing.Research(cmd.Context(), app, it)
			if ref != nil {
				fmt.Printf("%s: %s typical %.4g low %.4g high %.4g new %.4g (%d new, %d used, %d rejected)\n",
					it.GetString("name"), ref.GetString("status"), ref.GetFloat("typical"), ref.GetFloat("low"),
					ref.GetFloat("high"), ref.GetFloat("new_typical"), ref.GetInt("n_new"), ref.GetInt("n_used"), ref.GetInt("rejected"))
			}
			return err
		},
	}
	research.Flags().StringVar(&item, "item", "", "item id")

	reprice := &cobra.Command{
		Use: "reprice", Short: "Recompute deals for priced listings from the last --days days", SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := boot(); err != nil {
				return err
			}
			n, err := pipeline.Reprice(app, days)
			fmt.Printf("repriced %d listing(s)\n", n)
			return err
		},
	}
	reprice.Flags().IntVar(&days, "days", 30, "look back this many days")

	cmd.AddCommand(backfill, research, reprice)
	return cmd
}
