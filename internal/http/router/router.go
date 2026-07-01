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
	Sequence       *handlers.SequenceHandler
	PINAuth        *handlers.PINAuthHandler
	PlatformConfig   *handlers.PlatformConfigHandler
	CirculationRules *handlers.CirculationRuleHandler
	Holiday          *handlers.HolidayHandler
	AuthorizedValues *handlers.AuthorizedValueHandler
	Acquisition      *handlers.AcquisitionHandler
	AuthMiddleware   *authclient.AuthMiddleware
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
		r.Post("/api/v1/{tenant}/library/auth/pin/identify", d.PINAuth.IdentifyByPIN)
		r.Post("/api/v1/{tenant}/library/auth/pin/card", d.PINAuth.IdentifyByCard)
		r.Get("/api/v1/{tenant}/library/auth/pin/profiles", d.PINAuth.StaffProfiles)
		r.Get("/api/v1/{tenant}/library/auth/pin/branches", d.PINAuth.PINBranches)
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
			lib.Get("/auth/me/card.pdf", d.PINAuth.MyCard) // staff prints their own card
		}

		// Platform-owner-only configuration (credential-encryption key + integration secrets
		// like the ISBNdb API key). Platform owners bypass the subscription gate above.
		if d.PlatformConfig != nil {
			lib.Route("/platform", func(p chi.Router) {
				p.Use(libmw.RequirePlatformOwner())
				d.PlatformConfig.RegisterRoutes(p)
			})
		}
		// Per-route Django-style permission gates (library.{module}.{action}). `view` accepts
		// view|manage; `act` accepts the specific action|manage. library_admin (*) + superuser +
		// platform owner bypass. This is Layer-3 RBAC on top of the subscription/feature gates.
		view := func(mod string) func(http.Handler) http.Handler {
			return libmw.RequireServicePermission(d.RBAC, "library."+mod+".view", "library."+mod+".manage")
		}
		act := func(mod, action string) func(http.Handler) http.Handler {
			return libmw.RequireServicePermission(d.RBAC, "library."+mod+"."+action, "library."+mod+".manage")
		}

		lib.With(view("reports")).Get("/reports/summary", d.Reports.Summary)
		lib.With(view("reports")).Get("/reports/popular", d.Reports.Popular)
		lib.With(view("reports")).Get("/reports/circulation", d.Reports.Circulation)
		lib.With(view("reports")).Get("/reports/overdue", d.Reports.Overdue)

		// Catalog (OPAC search + bib/copy management) — feature gate + per-route permission gate.
		lib.Route("/catalog", func(c chi.Router) {
			c.Use(libmw.RequireFeature("library_catalog"))
			// Catalog (bibs) — members have catalog.view (OPAC); staff/admin add/change/delete.
			c.With(view("catalog")).Get("/bibs", d.Catalog.ListBibs)
			c.With(act("catalog", "add")).Post("/bibs", d.Catalog.CreateBib)
			c.With(view("catalog")).Get("/search", d.Catalog.Search)
			c.With(view("catalog")).Get("/facets", d.Catalog.Facets)
			c.With(view("catalog")).Get("/isbn/{isbn}", d.Catalog.ISBNLookup)
			c.With(view("catalog")).Get("/sru/search", d.Catalog.SRUSearch)
			c.With(view("catalog")).Get("/bibs/{id}", d.Catalog.GetBib)
			c.With(view("catalog")).Get("/bibs/{id}/marc.xml", d.Catalog.MarcXML)
			c.With(view("catalog")).Get("/bibs/{id}/marc.json", d.Catalog.MarcJSON)
			c.With(view("catalog")).Get("/bibs/{id}/recommendations", d.Catalog.Recommend)
			c.With(act("catalog", "change")).Put("/bibs/{id}", d.Catalog.UpdateBib)
			c.With(act("catalog", "delete")).Delete("/bibs/{id}", d.Catalog.DeleteBib)
			c.With(act("catalog", "change")).Post("/bibs/{id}/cover", d.Catalog.UploadCover)
			c.With(act("catalog", "add")).Post("/import/marc", d.Catalog.ImportMarc)
			// Cataloging dictionaries (author/publisher/place/subject pickers)
			c.With(view("catalog")).Get("/terms", d.Catalog.ListTerms)
			c.With(act("catalog", "add")).Post("/terms", d.Catalog.CreateTerm)
			// Collections
			c.With(view("catalog")).Get("/collections", d.Catalog.ListCollections)
			c.With(act("collections", "add")).Post("/collections", d.Catalog.CreateCollection)
			c.With(act("collections", "change")).Put("/collections/{id}", d.Catalog.UpdateCollection)
			c.With(act("collections", "delete")).Delete("/collections/{id}", d.Catalog.DeleteCollection)
			// Copies & holdings
			c.With(view("copies")).Get("/bibs/{id}/copies", d.Catalog.ListCopies)
			c.With(act("copies", "add")).Post("/copies", d.Catalog.CreateCopy)
			c.With(act("copies", "change")).Put("/copies/{id}", d.Catalog.UpdateCopy)
			c.With(view("copies")).Get("/copies/by-barcode/{barcode}", d.Catalog.GetCopyByBarcode)
			c.With(view("copies")).Get("/copies/{id}/label.pdf", d.Catalog.CopyLabel)
			// Transfers
			c.With(view("transfers")).Get("/transfers", d.Catalog.ListTransfers)
			c.With(act("transfers", "add")).Post("/transfers", d.Catalog.CreateTransfer)
			c.With(act("transfers", "receive")).Post("/transfers/{id}/receive", d.Catalog.ReceiveTransfer)
			// Stocktake
			c.With(view("stocktake")).Get("/stocktake", d.Catalog.ListStocktakes)
			c.With(act("stocktake", "add")).Post("/stocktake", d.Catalog.StartStocktake)
			c.With(act("stocktake", "scan")).Post("/stocktake/{id}/scan", d.Catalog.ScanStocktake)
			c.With(act("stocktake", "finalize")).Post("/stocktake/{id}/finalize", d.Catalog.FinalizeStocktake)
		})

		// Branches — staff read (copy form / branch filter); admin manages.
		lib.With(view("branches")).Get("/branches", d.Branch.List)
		lib.With(act("branches", "add")).Post("/branches", d.Branch.Create)
		lib.With(act("branches", "change")).Put("/branches/{id}", d.Branch.Update)

		// Members + tiers + policies + membership fees — feature gate + per-route permission gate.
		lib.Group(func(m chi.Router) {
			m.Use(libmw.RequireFeature("library_members"))
			m.With(view("members")).Get("/members", d.Member.ListMembers)
			m.With(act("members", "add")).Post("/members", d.Member.CreateMember)
			m.With(view("members")).Get("/members/{id}", d.Member.GetMember)
			m.With(act("members", "change")).Put("/members/{id}", d.Member.UpdateMember)
			m.With(act("members", "delete")).Delete("/members/{id}", d.Member.DeleteMember)
			m.With(view("members")).Get("/members/{id}/card.pdf", d.Member.MemberCard)
			m.With(view("members")).Get("/members/{id}/loans", d.Member.MemberLoans)
			m.With(view("members")).Get("/members/{id}/fines", d.Member.MemberFines)
			m.With(view("members")).Get("/members/{id}/notification-prefs", d.Member.GetNotificationPrefs)
			m.With(act("members", "change")).Put("/members/{id}/notification-prefs", d.Member.UpdateNotificationPrefs)
			m.With(view("member_tiers")).Get("/member-tiers", d.Member.ListTiers)
			m.With(act("member_tiers", "add")).Post("/member-tiers", d.Member.CreateTier)
			m.With(act("member_tiers", "change")).Put("/member-tiers/{id}", d.Member.UpdateTier)
			m.With(view("loan_policies")).Get("/loan-policies", d.Member.ListPolicies)
			m.With(act("loan_policies", "add")).Post("/loan-policies", d.Member.CreatePolicy)
			m.With(act("loan_policies", "change")).Put("/loan-policies/{id}", d.Member.UpdatePolicy)
			m.With(view("membership_fees")).Get("/membership-fees", d.Membership.List)
			m.With(act("membership_fees", "add")).Post("/members/{id}/membership-fee", d.Membership.Issue)
			m.With(act("membership_fees", "pay")).Post("/membership-fees/{id}/pay", d.Membership.Pay)
		})

		// Circulation (checkout/return/renew/mark-lost) — feature gate + per-action permission gate.
		lib.Group(func(c chi.Router) {
			c.Use(libmw.RequireFeature("library_circulation"))
			c.With(act("circulation", "checkout")).Post("/circulation/checkout", d.Circulation.Checkout)
			c.With(act("circulation", "return")).Post("/circulation/return", d.Circulation.Return)
			c.With(act("circulation", "renew")).Post("/circulation/renew/{loan_id}", d.Circulation.Renew)
			c.With(act("circulation", "manage")).Post("/circulation/loans/{loan_id}/mark-lost", d.Circulation.MarkLost)
			c.With(act("circulation", "manage")).Post("/circulation/loans/{loan_id}/recall", d.Circulation.Recall)
			c.With(view("circulation")).Get("/circulation/loans", d.Circulation.ListLoans)
		})

		// Holds & reservations — feature gate + per-action permission gate.
		lib.Group(func(h chi.Router) {
			h.Use(libmw.RequireFeature("library_holds"))
			h.With(view("holds")).Get("/holds", d.Hold.List)
			h.With(act("holds", "place")).Post("/holds", d.Hold.Place)
			h.With(act("holds", "change")).Post("/holds/{id}/ready", d.Hold.MarkReady)
			h.With(act("holds", "delete")).Delete("/holds/{id}", d.Hold.Cancel)
		})

		// Fines & fees — feature gate + per-action permission gate.
		lib.Group(func(f chi.Router) {
			f.Use(libmw.RequireFeature("library_fines"))
			f.With(view("fines")).Get("/fines", d.Fine.List)
			f.With(act("membership_fees", "add")).Post("/fines/membership", d.Fine.AssessMembershipFee)
			f.With(act("fines", "waive")).Post("/fines/{id}/waive", d.Fine.Waive)
			f.With(act("fines", "pay")).Post("/fines/{id}/pay", d.Fine.Pay)
		})

		// E-books & controlled digital lending — feature gate + per-action permission gate.
		lib.Group(func(e chi.Router) {
			e.Use(libmw.RequireFeature("library_ebooks"))
			e.With(view("ebooks")).Get("/ebooks", d.Ebook.List)
			e.With(act("ebooks", "add")).Post("/ebooks", d.Ebook.Create)
			e.With(act("ebooks", "lend")).Post("/ebooks/{id}/lend", d.Ebook.Lend)
			e.With(view("ebooks")).Get("/ebooks/{id}/read", d.Ebook.Read)
			e.With(view("ebooks")).Post("/ebooks/loans/{id}/position", d.Ebook.SavePosition)
			e.With(act("ebooks", "change")).Post("/ebooks/{id}/purchase", d.Ebook.Purchase)
			e.With(view("ebooks")).Get("/ebooks/{id}/download", d.Ebook.Download)
		})

		// RBAC / team — admin only (team.view / team.manage).
		lib.With(view("team")).Get("/rbac/roles", d.RBACHandler.ListRoles)
		lib.With(act("team", "manage")).Post("/rbac/roles", d.RBACHandler.CreateRole)
		lib.With(act("team", "manage")).Put("/rbac/roles/{id}", d.RBACHandler.UpdateRole)
		lib.With(act("team", "manage")).Delete("/rbac/roles/{id}", d.RBACHandler.DeleteRole)
		lib.With(view("team")).Get("/rbac/permissions", d.RBACHandler.ListPermissions)
		lib.With(view("team")).Get("/team", d.RBACHandler.ListTeam)
		if d.PINAuth != nil {
			lib.With(view("team")).Get("/team/{user_id}/card.pdf", d.PINAuth.StaffCard)
		}
		lib.With(act("team", "manage")).Put("/team/{user_id}/roles", d.RBACHandler.AssignRoles)
		lib.With(act("team", "manage")).Put("/team/{user_id}/branches", d.RBACHandler.AssignBranches)

		// Settings — document-sequence configuration (membership_no, accession_no, …).
		if d.Sequence != nil {
			lib.With(view("settings")).Get("/settings/sequences", d.Sequence.List)
			lib.With(act("settings", "manage")).Put("/settings/sequences/{kind}", d.Sequence.Update)
		}

		// Admin — 3D circulation rules matrix (branch × tier × format).
		if d.CirculationRules != nil {
			lib.With(view("settings")).Get("/admin/circulation-rules", d.CirculationRules.List)
			lib.With(act("settings", "manage")).Post("/admin/circulation-rules", d.CirculationRules.Create)
			lib.With(act("settings", "manage")).Put("/admin/circulation-rules/{id}", d.CirculationRules.Update)
			lib.With(act("settings", "manage")).Delete("/admin/circulation-rules/{id}", d.CirculationRules.Delete)
		}

		// Admin — library holiday calendar.
		if d.Holiday != nil {
			lib.With(view("settings")).Get("/admin/holidays", d.Holiday.List)
			lib.With(act("settings", "manage")).Post("/admin/holidays", d.Holiday.Create)
			lib.With(act("settings", "manage")).Put("/admin/holidays/{id}", d.Holiday.Update)
			lib.With(act("settings", "manage")).Delete("/admin/holidays/{id}", d.Holiday.Delete)
		}

		// Admin — authorized values (controlled vocabulary).
		if d.AuthorizedValues != nil {
			lib.With(view("settings")).Get("/admin/authorized-values/categories", d.AuthorizedValues.ListCategories)
			lib.With(view("settings")).Get("/admin/authorized-values", d.AuthorizedValues.List)
			lib.With(act("settings", "manage")).Post("/admin/authorized-values", d.AuthorizedValues.Create)
			lib.With(act("settings", "manage")).Put("/admin/authorized-values/{id}", d.AuthorizedValues.Update)
			lib.With(act("settings", "manage")).Delete("/admin/authorized-values/{id}", d.AuthorizedValues.Delete)
		}

		// Acquisitions — vendors, budgets/funds, purchase orders, invoices.
		if d.Acquisition != nil {
			lib.With(view("acquisitions")).Get("/acquisitions/vendors", d.Acquisition.ListVendors)
			lib.With(act("acquisitions", "add")).Post("/acquisitions/vendors", d.Acquisition.CreateVendor)
			lib.With(view("acquisitions")).Get("/acquisitions/vendors/{id}", d.Acquisition.GetVendor)
			lib.With(act("acquisitions", "change")).Put("/acquisitions/vendors/{id}", d.Acquisition.UpdateVendor)

			lib.With(view("acquisitions")).Get("/acquisitions/budgets", d.Acquisition.ListBudgets)
			lib.With(act("acquisitions", "add")).Post("/acquisitions/budgets", d.Acquisition.CreateBudget)
			lib.With(act("acquisitions", "change")).Put("/acquisitions/budgets/{id}", d.Acquisition.UpdateBudget)
			lib.With(view("acquisitions")).Get("/acquisitions/budgets/{budget_id}/funds", d.Acquisition.ListFunds)
			lib.With(act("acquisitions", "add")).Post("/acquisitions/budgets/{budget_id}/funds", d.Acquisition.CreateFund)

			lib.With(view("acquisitions")).Get("/acquisitions/orders", d.Acquisition.ListOrders)
			lib.With(act("acquisitions", "add")).Post("/acquisitions/orders", d.Acquisition.CreateOrder)
			lib.With(view("acquisitions")).Get("/acquisitions/orders/{id}", d.Acquisition.GetOrder)
			lib.With(act("acquisitions", "change")).Put("/acquisitions/orders/{id}", d.Acquisition.UpdateOrder)
			lib.With(act("acquisitions", "change")).Post("/acquisitions/orders/{id}/submit", d.Acquisition.SubmitOrder)
			lib.With(act("acquisitions", "add")).Post("/acquisitions/orders/{id}/lines", d.Acquisition.AddLine)
			lib.With(act("acquisitions", "change")).Post("/acquisitions/orders/{id}/lines/{line_id}/receive", d.Acquisition.ReceiveLine)

			lib.With(view("acquisitions")).Get("/acquisitions/invoices", d.Acquisition.ListInvoices)
			lib.With(view("acquisitions")).Get("/acquisitions/invoices/{id}", d.Acquisition.GetInvoice)
			lib.With(act("acquisitions", "add")).Post("/acquisitions/invoices", d.Acquisition.CreateInvoice)
		}
	})

	return r
}
