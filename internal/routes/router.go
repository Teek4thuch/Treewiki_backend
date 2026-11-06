package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"treewiki/wiki_backend/internal/controllers"
)

func NewRouter(health *controllers.HealthController, plants *controllers.PlantController) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	_ = router.SetTrustedProxies(nil)
	api := router.Group("/api/v1")
	api.GET("/health", health.Liveness)
	api.GET("/ready", health.Readiness)
	api.GET("/plants", plants.List)
	api.GET("/plants/:id", plants.Species)
	api.GET("/taxa/root", plants.Root)
	api.GET("/taxa/:id", plants.Detail)
	api.GET("/taxa/:id/children", plants.Children)
	router.NoRoute(func(c *gin.Context) { c.JSON(http.StatusNotFound, gin.H{"error": "not_found"}) })
	return router
}
