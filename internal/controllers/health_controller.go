package controllers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"treewiki/wiki_backend/internal/models"
)

type ReadinessChecker interface {
	CheckReadiness(context.Context) error
}

type HealthController struct {
	service ReadinessChecker
}

func NewHealthController(service ReadinessChecker) *HealthController {
	return &HealthController{service: service}
}

func (h *HealthController) Liveness(c *gin.Context) {
	c.JSON(http.StatusOK, models.HealthResponse{Status: "ok", Service: "treewiki"})
}

func (h *HealthController) Readiness(c *gin.Context) {
	if err := h.service.CheckReadiness(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, models.HealthResponse{
			Status: "unavailable", Database: "unavailable",
		})
		return
	}
	c.JSON(http.StatusOK, models.HealthResponse{Status: "ok", Database: "ok"})
}
