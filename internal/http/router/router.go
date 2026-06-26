package router

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.uber.org/zap"

	httpware "github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"

	handlers "github.com/bengobox/library-service/internal/http/handlers"
	libmw "github.com/bengobox/library-service/internal/http/middleware"
	"github.com/bengobox/library-service/internal/modules/rbac"
)

// Deps bundles everything the router mounts.
type Deps struct {
	Log            *zap.Logger
	Health         *handlers.HealthHandler
	Auth           *handlers.AuthHandler
	Catalog        *handlers.CatalogHandler
	Branch         *handlers.BranchHandler
	Member         *handlers.MemberHandler
	Circulation    *handlers.CirculationHandler
	Hold           *handlers.HoldHandler
	Fine           *handlers.FineHandler
	Ebook          *handlers.EbookHandler
	Reports        *handlers.ReportsHandler
	RBACHandler    *handlers.RBACHandler
	Membership     *handlers.MembershipHandler
	PINAuth        *handlers.PINAuthHandler
	AuthMiddleware *authclient.AuthMiddleware
	RBAC           *rbac.Service
	AllowedOrigins []string
	MediaRoot      string
}

// New builds the chi router with the standard middleware stack and all library routes.
func New(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(httpware.RequestID)
	r.Use(httpware.Logging(d.Log))
	r.Use(httpware.Recover(d.Log))
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(middleware.RequestSize(50 << 20)) // 50 MB (e-book uploads)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   d.AllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Origin", "X-Request-ID", "X-Tenant-ID", "X-Tenant-Slug", "X-API-Key", "Idempotency-Key", "X-Outlet-ID"},
		ExposedHeaders:   []string{"Link", "Retry-After"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/healthz", d.Health.Liveness)
	r.Get("/readyz", d.Health.Readiness)
	r.Get("/metrics", d.Health.Metrics)

	// API docs (Swagger UI + OpenAPI spec). Base URL redirects to the docs page.
	r.Get("/v1/docs", handlers.SwaggerUI)
	r.Get("/v1/docs/*", handlers.SwaggerUI)
	r.Get("/api/v1/openapi.json", handlers.OpenAPIJSON)
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/v1/docs/", http.StatusMovedPermanently)
	})
	if d.MediaRoot != "" {
		r.Handle("/media/*", http.StripPrefix("/media", http.FileServer(http.Dir(d.MediaRoot))))
	}

	// Public PIN/terminal auth (no SSO) — desk/kiosk quick login.
	if d.PINAuth != nil {
		r.Post("/api/v1/{tenant}/library/auth/pin", d.PINAuth.Login)
		r.Get("/api/v1/{tenant}/library/auth/pin/profiles", d.PINAuth.StaffProfiles)
	}

	r.Route("/api/v1/{tenant}/library", func(lib chi.Router) {
		// Accept SSO JWTs and terminal PIN JWTs uniformly.
		if d.PINAuth != nil {
			lib.Use(handlers.RequireAnyAuth(d.PINAuth.Secret(), d.AuthMiddleware))
		} else if d.AuthMiddleware != nil {
			lib.Use(d.AuthMiddleware.RequireAuth)
		}
		// JIT user provisioning (heals existing users on every request).
		if d.RBAC != nil {
			lib.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if claims, ok := authclient.ClaimsFromContext(req.Context()); ok && claims != nil {
						if err := d.RBAC.EnsureUserFromToken(req.Context(), claims); err != nil {
							d.Log.Warn("jit provisioning failed", zap.Error(err))
						}
					}
					next.ServeHTTP(w, req)
				})
			})
		}
		// Mutations-only subscription gate (GET always passes).
		lib.Use(libmw.RequireActiveSubscriptionForMutations)

		lib.Get("/auth/me", d.Auth.Me)
		if d.PINAuth != nil {
			lib.Post("/auth/pin/set", d.PINAuth.SetPIN)
		}
		lib.Get("/reports/summary", d.Reports.Summary)

		// Catalog
		lib.Route("/catalog", func(c chi.Router) {
			c.Get("/bibs", d.Catalog.ListBibs)
			c.Post("/bibs", d.Catalog.CreateBib)
			c.Get("/search", d.Catalog.Search)
			c.Get("/facets", d.Catalog.Facets)
			c.Get("/isbn/{isbn}", d.Catalog.ISBNLookup)
			c.Get("/bibs/{id}/marc.xml", d.Catalog.MarcXML)
			c.Get("/bibs/{id}/marc.json", d.Catalog.MarcJSON)
			c.Post("/import/marc", d.Catalog.ImportMarc)
			c.Get("/bibs/{id}", d.Catalog.GetBib)
			c.Put("/bibs/{id}", d.Catalog.UpdateBib)
			c.Delete("/bibs/{id}", d.Catalog.DeleteBib)
			c.Get("/bibs/{id}/copies", d.Catalog.ListCopies)
			c.Post("/copies", d.Catalog.CreateCopy)
			c.Put("/copies/{id}", d.Catalog.UpdateCopy)
			c.Get("/copies/by-barcode/{barcode}", d.Catalog.GetCopyByBarcode)
			c.Get("/copies/{id}/label.pdf", d.Catalog.CopyLabel)
			c.Get("/transfers", d.Catalog.ListTransfers)
			c.Post("/transfers", d.Catalog.CreateTransfer)
			c.Post("/transfers/{id}/receive", d.Catalog.ReceiveTransfer)
			c.Get("/stocktake", d.Catalog.ListStocktakes)
			c.Post("/stocktake", d.Catalog.StartStocktake)
			c.Post("/stocktake/{id}/scan", d.Catalog.ScanStocktake)
			c.Post("/stocktake/{id}/finalize", d.Catalog.FinalizeStocktake)
			c.Get("/bibs/{id}/recommendations", d.Catalog.Recommend)
			c.Get("/sru/search", d.Catalog.SRUSearch)
		})

		// Branches
		lib.Get("/branches", d.Branch.List)
		lib.Post("/branches", d.Branch.Create)
		lib.Put("/branches/{id}", d.Branch.Update)

		// Members + tiers + policies
		lib.Get("/members", d.Member.ListMembers)
		lib.Post("/members", d.Member.CreateMember)
		lib.Get("/members/{id}", d.Member.GetMember)
		lib.Put("/members/{id}", d.Member.UpdateMember)
		lib.Get("/members/{id}/loans", d.Member.MemberLoans)
		lib.Get("/members/{id}/fines", d.Member.MemberFines)
		lib.Get("/member-tiers", d.Member.ListTiers)
		lib.Post("/member-tiers", d.Member.CreateTier)
		lib.Put("/member-tiers/{id}", d.Member.UpdateTier)
		lib.Get("/loan-policies", d.Member.ListPolicies)
		lib.Post("/loan-policies", d.Member.CreatePolicy)
		lib.Get("/membership-fees", d.Membership.List)
		lib.Post("/members/{id}/membership-fee", d.Membership.Issue)
		lib.Post("/membership-fees/{id}/pay", d.Membership.Pay)

		// Circulation
		lib.Post("/circulation/checkout", d.Circulation.Checkout)
		lib.Post("/circulation/return", d.Circulation.Return)
		lib.Post("/circulation/renew/{loan_id}", d.Circulation.Renew)
		lib.Get("/circulation/loans", d.Circulation.ListLoans)

		// Holds
		lib.Get("/holds", d.Hold.List)
		lib.Post("/holds", d.Hold.Place)
		lib.Delete("/holds/{id}", d.Hold.Cancel)

		// Fines
		lib.Get("/fines", d.Fine.List)
		lib.Post("/fines/{id}/waive", d.Fine.Waive)
		lib.Post("/fines/{id}/pay", d.Fine.Pay)

		// Ebooks
		lib.Get("/ebooks", d.Ebook.List)
		lib.Post("/ebooks", d.Ebook.Create)
		lib.Post("/ebooks/{id}/lend", d.Ebook.Lend)
		lib.Get("/ebooks/{id}/read", d.Ebook.Read)
		lib.Post("/ebooks/loans/{id}/position", d.Ebook.SavePosition)
		lib.Post("/ebooks/{id}/purchase", d.Ebook.Purchase)
		lib.Get("/ebooks/{id}/download", d.Ebook.Download)

		// RBAC / team
		lib.Get("/rbac/roles", d.RBACHandler.ListRoles)
		lib.Get("/rbac/permissions", d.RBACHandler.ListPermissions)
		lib.Get("/team", d.RBACHandler.ListTeam)
		lib.Put("/team/{user_id}/roles", d.RBACHandler.AssignRoles)
	})

	return r
}
