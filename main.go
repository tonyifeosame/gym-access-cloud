package main

import (
	"log"
	"os"

	"gym-access-api/database"
	"gym-access-api/handlers"
	"gym-access-api/middleware"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Set Gin mode
	ginMode := os.Getenv("GIN_MODE")
	if ginMode == "" {
		ginMode = "release"
	}
	gin.SetMode(ginMode)

	// Connect to database
	dbConfig := database.GetConfigFromEnv()
	if err := database.Connect(dbConfig); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	// Create Gin router
	r := gin.Default()

	// Apply middleware
	r.Use(middleware.CORSMiddleware())
	r.Use(middleware.LoggingMiddleware())

	// Health check endpoint (no auth required)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "healthy",
			"service": "Gym Access API",
		})
	})

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
	}

	// Start server
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8080"
	}
	
	log.Printf("Starting Gym Access API server on port %s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
