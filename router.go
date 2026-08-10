package main

import (
	"access-terminal-cloud-api/handlers"
	"access-terminal-cloud-api/middleware"

	"github.com/gin-gonic/gin"
)

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

	r.Use(gin.Recovery())
	r.Use(middleware.RequestIDMiddleware())
	r.Use(middleware.CORSMiddleware())
	r.Use(middleware.LoggingMiddleware())

	// Health check endpoint (no auth required)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":  "healthy",
			"service": "Access Terminal Cloud API",
		})
	})

	// Monitoring endpoints (no auth required; /metrics honours METRICS_TOKEN)
	r.GET("/health/live", handlers.HealthLive)
	r.GET("/health/ready", handlers.HealthReady)
	r.GET("/health/maintenance", handlers.HealthMaintenance)
	r.GET("/metrics", handlers.Metrics)

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
