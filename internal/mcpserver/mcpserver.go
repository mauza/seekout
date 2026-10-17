// Package mcpserver exposes seekout to agents (Hermes) as a Model Context
// Protocol server over streamable HTTP at /mcp.
//
// Callers authenticate with a PocketBase users auth token as a bearer token
// (mint a long-lived one with `seekout mcp-token <email>`). Tools run
// server-side, so PocketBase's collection API rules do NOT apply here: every
// tool scopes reads and writes to the token's user itself, mirroring the
// rules in migrations/ (sources and listings are shared between signed-in
// users; seekers, alerters and alerts are per-user).
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
)

// Handler returns the /mcp HTTP handler: bearer-token auth in front of a
// stateless streamable transport. Stateless keeps pod restarts invisible to
// clients — there is no session to lose.
func Handler(app core.App, version string) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "seekout", Version: version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	registerTools(server, app)

	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
	return auth.RequireBearerToken(verifier(app), nil)(h)
}

const instructions = `Seekout watches listing sources (RSS feeds, the Utah MLS) and alerts when a new listing matches a seeker.
- A source is a shared poller; a listing is one item it produced (homes, cars, classifieds).
- A seeker is the user's saved search: optional kind, optional source allow-list, conditions (hard ones must all hold; soft ones below), and the alerters to notify.
- Conditions are {field, op, value}. field is a listing column (title, price, url, description, kind, published) or a dot path into the listing's attrs (e.g. beds, baths, sqft, city). op is one of = != > < >= <= in contains.
- A condition can be soft: {"field":"garage_capacity","op":">=","value":3,"soft":true,"floor":2,"best":4,"weight":2}. Soft conditions trade off instead of each having to hold: each scores 0 at value (the target), up to +1 at best, down to -1 at floor (missing values score -1), and past floor the listing fails. A listing matches when all hard conditions hold and the weighted soft scores sum to >= 0, so a shortfall on one must be paid for by others. Only > >= < <= can be soft.
- Seekers only alert on listings that arrive after they exist; use search_listings to look at what is already stored (with soft conditions it returns each listing's score and why).`

// verifier accepts PocketBase auth tokens for verified users.
func verifier(app core.App) auth.TokenVerifier {
	return func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		user, err := app.FindAuthRecordByToken(token, core.TokenTypeAuth)
		if err != nil || user.Collection().Name != "users" {
			return nil, auth.ErrInvalidToken
		}
		if !user.Verified() {
			return nil, fmt.Errorf("%w: email not verified", auth.ErrInvalidToken)
		}
		claims, err := security.ParseUnverifiedJWT(token)
		if err != nil {
			return nil, auth.ErrInvalidToken
		}
		exp, _ := claims["exp"].(float64)
		return &auth.TokenInfo{UserID: user.Id, Expiration: time.Unix(int64(exp), 0)}, nil
	}
}

// userID is the authenticated caller. The middleware guarantees one, so a
// missing value is a wiring bug, reported rather than treated as anonymous.
func userID(req *mcp.CallToolRequest) (string, error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
		return "", errors.New("unauthenticated")
	}
	return req.Extra.TokenInfo.UserID, nil
}
