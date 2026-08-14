package main

import (
	"log"
	"os"
	"strings"

	"access-terminal-cloud-api/handlers"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// configureTrustedProxies decides whose X-Forwarded-For gin is allowed to
// believe.
//
// Gin's default is to trust EVERY proxy, which means it takes the last hop of
// whatever X-Forwarded-For arrives and reports that as c.ClientIP(). Reached
// directly, a client can therefore choose its own apparent address -- and this
// API writes c.ClientIP() onto the device row at registration and heartbeat,
// so the recorded address of a terminal was spoofable. The Nginx site in
// deploy/ overwrites the header rather than appending to contain that at the
// edge, but the edge is not the only way in and the mitigation belonged here.
//
// TRUSTED_PROXIES is a comma-separated list of IPs or CIDRs. The default is
// loopback, matching the deployment: Nginx runs on the host and the API is
// published on 127.0.0.1 only. Set it explicitly when the proxy is elsewhere --
// a container on the compose network, or a load balancer on a private subnet.
//
// TRUSTED_PROXIES=none trusts nobody: ClientIP() then always reports the peer
// address and X-Forwarded-For is ignored entirely. That is the right setting if
// the API is ever exposed without a proxy in front of it.
func configureTrustedProxies(r *gin.Engine) {
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))

	if strings.EqualFold(raw, "none") {
		if err := r.SetTrustedProxies(nil); err != nil {
			log.Fatalf("TRUSTED_PROXIES: %v", err)
		}
		log.Println("Trusted proxies: none (X-Forwarded-For ignored)")
		return
	}

	proxies := []string{"127.0.0.1", "::1"}
	if raw != "" {
		proxies = proxies[:0]
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				proxies = append(proxies, p)
			}
		}
	}

	// A malformed entry is fatal rather than logged and skipped. Falling back to
	// gin's trust-everything default because a CIDR had a typo is exactly the
	// silent downgrade this function exists to prevent.
	if err := r.SetTrustedProxies(proxies); err != nil {
		log.Fatalf("TRUSTED_PROXIES is not a valid list of IPs/CIDRs: %v", err)
	}
	log.Printf("Trusted proxies: %s", strings.Join(proxies, ", "))
}

// NewRouter builds the HTTP routing table.
//
// Separated from main so the integration tests exercise the same router the
// server runs, including the middleware chain. Route tables have a habit of
// drifting from the handlers they are supposed to expose, and an auth boundary
// that is only correct because a test called the handler function directly is
// not an auth boundary at all -- the tests here go through the middleware.
func NewRouter() *gin.Engine {
	// gin.New rather than gin.Default: gin.Default installs its own request
	// logger, which would print a second line per request alongside the one that
	// carries the request id and device identity.
	r := gin.New()

	configureTrustedProxies(r)

	r.Use(gin.Recovery())
	r.Use(middleware.RequestIDMiddleware())
	r.Use(middleware.CORSMiddleware())
	r.Use(middleware.LoggingMiddleware())

	// Health check endpoint (no auth required).
	//
	// The existing status/service fields are unchanged -- terminals and uptime
	// checks already parse this response, and version/commit are additive.
	r.GET("/health", func(c *gin.Context) {
		build := handlers.Build()
		c.JSON(200, gin.H{
			"status":  "healthy",
			"service": "Access Terminal Cloud API",
			"version": build.Version,
			"commit":  build.Commit,
		})
	})

	// Monitoring endpoints (no auth required; /metrics honours METRICS_TOKEN)
	r.GET("/health/live", handlers.HealthLive)
	r.GET("/health/ready", handlers.HealthReady)
	r.GET("/health/maintenance", handlers.HealthMaintenance)
	r.GET("/metrics", handlers.Metrics)

	// Operator authentication, /api/v1/auth/*.
	//
	// A SEPARATE group from the v1 tree below, which authenticates with the site
	// API key. That key is the provisioning secret -- it registers terminals and
	// rotates their credentials -- so a browser must never hold it, and these
	// routes never read it. The two credential classes share a URL prefix and
	// nothing else.
	//
	// Domain-neutral by construction: an operator, a company, a role and a
	// session. Nothing here knows or asks what the platform is being used for.
	auth := r.Group("/api/v1/auth")
	{
		// ONE limiter shared by both credential endpoints, so an attacker
		// cannot get a second allowance by alternating between them.
		credentialLimit := middleware.LoginRateLimiter()

		auth.POST("/login", credentialLimit, handlers.OperatorLogin)

		session := auth.Group("")
		session.Use(middleware.OperatorAuthMiddleware())
		{
			session.GET("/me", handlers.OperatorMe)
			session.POST("/logout", middleware.RequireCSRF(), handlers.OperatorLogout)
			session.POST("/password", credentialLimit, middleware.RequireCSRF(),
				handlers.OperatorChangePassword)
		}
	}

	// Operator console, /api/v1/console/*.
	//
	// The dashboard's API. Session-authenticated like /api/v1/auth above, and
	// like it, entirely separate from the site-key and device trees below --
	// nothing here reads X-API-Key, and nothing here returns one.
	//
	// The chain on every route: a session, then CSRF on anything unsafe, then
	// the minimum role, then a site grant where the path names a site. Routes
	// are grouped by the role they require so that a new route cannot be added
	// without landing inside one of those groups.
	console := r.Group("/api/v1/console")
	console.Use(middleware.OperatorAuthMiddleware())
	{
		// Reads: any authenticated operator in the company.
		read := console.Group("")
		read.Use(middleware.RequireRole(models.RoleViewer))
		{
			read.GET("/company", handlers.ConsoleGetCompany)
			read.GET("/sites", handlers.ConsoleListSites)
			read.GET("/applications", handlers.ConsoleListApplications)

			read.GET("/terminals", handlers.ConsoleListTerminals)
			read.GET("/terminals/summary", handlers.ConsoleTerminalSummary)

			// RequireTerminalGrant resolves :serial to the site the terminal
			// is at and applies the same rule RequireSiteGrant does. Without
			// it a scoped operator could not SEE another site's terminal in
			// the list but could still read it by naming its serial, which is
			// printed on the hardware.
			read.GET("/terminals/:serial", middleware.RequireTerminalGrant("serial"),
				handlers.ConsoleGetTerminal)

			read.GET("/people", handlers.ConsoleListPeople)
			read.GET("/people/:external_id", handlers.ConsoleGetPerson)

			// Site-scoped reads. RequireSiteGrant resolves :site_id inside the
			// operator's company and puts it in the context, so a site in
			// another tenant is a 404 and one they are not scoped to is a 403.
			read.GET("/sites/:site_id", middleware.RequireSiteGrant("site_id"),
				handlers.ConsoleGetSite)
			read.GET("/sites/:site_id/settings", middleware.RequireSiteGrant("site_id"),
				handlers.GetSiteSettings)
		}

		// Day-to-day writes.
		write := console.Group("")
		write.Use(middleware.RequireCSRF(), middleware.RequireRole(models.RoleManager))
		{
			write.POST("/people", handlers.ConsoleCreatePerson)
			write.PUT("/people/:external_id", handlers.ConsoleUpdatePerson)
			write.DELETE("/people/:external_id", handlers.ConsoleDeletePerson)

			// Changing what a terminal does is a site operation, gated on the
			// grant to that terminal's site exactly as the read above is.
			write.PUT("/terminals/:serial/application-mode",
				middleware.RequireTerminalGrant("serial"), handlers.ConsoleSetTerminalMode)

			// GetSiteSettings/UpdateSiteSettings are the SAME handlers the
			// site-key API uses. Both read site_id from the context, which
			// RequireSiteGrant sets to an authorized site exactly as the
			// site-key middleware sets it to the credential's own site. The
			// write also enqueues a SETTINGS sync job for every terminal at
			// that site -- business logic worth reusing, not restating.
			write.PUT("/sites/:site_id/settings", middleware.RequireSiteGrant("site_id"),
				handlers.UpdateSiteSettings)
		}

		// Operator administration, and site lifecycle.
		//
		// Sites are ADMIN rather than MANAGER: creating one mints a
		// PROVISIONING CREDENTIAL and retiring one stops doors opening. Neither
		// is day-to-day work, which is what the MANAGER gate above is for.
		//
		// The grant rule lands correctly here without extra machinery. ADMIN and
		// OWNER are never site-scoped, so "an operator scoped to Site A creates
		// Site B" cannot arise -- and the routes naming a site still go through
		// RequireSiteGrant, which resolves it inside the caller's company and
		// answers 404 for anybody else's.
		admin := console.Group("")
		admin.Use(middleware.RequireCSRF(), middleware.RequireRole(models.RoleAdmin))
		{
			admin.POST("/sites", handlers.ConsoleCreateSite)
			admin.PUT("/sites/:site_id", middleware.RequireSiteGrant("site_id"),
				handlers.ConsoleUpdateSite)
			admin.DELETE("/sites/:site_id", middleware.RequireSiteGrant("site_id"),
				handlers.ConsoleRetireSite)

			// Rotation is a POST to a sub-resource rather than a PUT on the
			// site: it does not set a value the caller supplied, it mints one
			// and returns it. Making that a metadata update would invite a
			// client to send it twice.
			admin.POST("/sites/:site_id/api-key", middleware.RequireSiteGrant("site_id"),
				handlers.ConsoleRotateSiteAPIKey)

			admin.GET("/operators", handlers.ConsoleListOperators)
			admin.GET("/operators/:operator_id", handlers.ConsoleGetOperator)
			admin.GET("/operators/:operator_id/sites", handlers.ConsoleGetOperatorSites)
			admin.POST("/operators", handlers.ConsoleCreateOperator)
			admin.PUT("/operators/:operator_id", handlers.ConsoleUpdateOperator)
			admin.PUT("/operators/:operator_id/sites", handlers.ConsoleSetOperatorSites)
			admin.DELETE("/operators/:operator_id", handlers.ConsoleDeleteOperator)
		}

		// Which capabilities the company has at all. OWNER only: this decides
		// what the whole company's deployment is for, which is a different kind
		// of decision from running it day to day.
		owner := console.Group("")
		owner.Use(middleware.RequireCSRF(), middleware.RequireRole(models.RoleOwner))
		{
			owner.PUT("/applications/:code", handlers.ConsoleSetApplication)
		}
	}

	// API v1 routes with authentication
	v1 := r.Group("/api/v1")
	v1.Use(middleware.AuthMiddleware())
	{
		// Member endpoints
		members := v1.Group("/members")
		{
			members.GET("", handlers.GetMembers)
			members.GET("/:id", handlers.GetMember)
			members.POST("", handlers.CreateMember)
			members.PUT("/:id", handlers.UpdateMember)
			members.DELETE("/:id", handlers.DeleteMember)
			members.GET("/changes", handlers.GetMemberChanges)
		}

		// Access endpoints
		access := v1.Group("/access")
		{
			access.GET("/:member_id", handlers.CheckAccess)
			access.POST("/log", handlers.LogAccess)
			access.GET("/logs", handlers.GetAccessLogs)
			access.GET("/logs/:member_id", handlers.GetMemberAccessLogs)
		}

		// Enrollment endpoints
		enrollment := v1.Group("/enrollment")
		{
			enrollment.POST("/start", handlers.StartEnrollment)
			enrollment.GET("/pending", handlers.GetPendingEnrollments)
			enrollment.POST("/result", handlers.SubmitEnrollmentResult)
		}

		// Site settings endpoints
		sites := v1.Group("/sites")
		{
			sites.GET("/settings", handlers.GetSiteSettings)
			sites.PUT("/settings", handlers.UpdateSiteSettings)
		}

		// Device registration: authenticated with the site API key, because the
		// device does not have a credential of its own yet.
		v1.POST("/devices/register", handlers.RegisterDevice)

		// Fleet inventory and operator actions
		v1.GET("/devices", handlers.ListDevices)
		v1.GET("/devices/summary", handlers.GetFleetSummary)
		v1.POST("/devices/:serial/resync", handlers.ResyncDevice)

		// Firmware catalog (inventory only -- no OTA)
		firmware := v1.Group("/firmware")
		{
			firmware.GET("", handlers.ListFirmwareVersions)
			firmware.POST("", handlers.CreateFirmwareVersion)
			firmware.PUT("/:id/current", handlers.SetCurrentFirmware)
		}
	}

	// Device endpoints authenticate as the device itself, not as the site
	deviceAPI := r.Group("/api/v1/devices")
	deviceAPI.Use(middleware.DeviceAuthMiddleware())
	{
		deviceAPI.POST("/heartbeat", handlers.DeviceHeartbeat)
		deviceAPI.GET("/settings", handlers.GetDeviceSettings)
		deviceAPI.GET("/jobs", handlers.GetDeviceJobs)
		deviceAPI.POST("/jobs/:id/complete", handlers.CompleteDeviceJob)

		// Enrolment, reachable with the DEVICE credential.
		//
		// These are the same two handlers the operator API exposes, mounted a
		// second time behind device authentication. Both read company_id from
		// the context and DeviceAuthMiddleware sets it, so nothing about their
		// behaviour changes -- only who is allowed to call them.
		//
		// WHY THIS EXISTS. A terminal is where enrolment physically happens: an
		// operator stands at the door and presents a finger. Until now the only
		// way to report that was `POST /enrollment/result`, which is
		// site-key-authenticated -- so a terminal could only close the loop by
		// carrying the site's PROVISIONING SECRET, the credential that can
		// register devices and rotate their keys. Putting that on every terminal
		// to report an enrolment inverts the entire point of per-device
		// credentials.
		deviceAPI.GET("/enrollment/pending", handlers.GetPendingEnrollments)
		deviceAPI.POST("/enrollment/result", handlers.SubmitEnrollmentResult)

		// Door events, reported with the device's own credential.
		//
		// NOT the same handler as POST /access/log above. That one trusts the
		// site key and reads site_name from it; this one takes company, site AND
		// device from the authenticated terminal, so there is no parameter
		// through which a device could write a log against another site.
		deviceAPI.POST("/access/log", handlers.LogDeviceAccess)
	}

	return r
}
