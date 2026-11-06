package controllers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"treewiki/wiki_backend/internal/models"
	"treewiki/wiki_backend/internal/repositories"
)

type CatalogReader interface {
	List(context.Context, string, string, bool) (models.TaxonPage, error)
	Children(context.Context, string, string) (models.TaxonPage, error)
	Root(context.Context) (models.Taxon, error)
	Detail(context.Context, string) (models.PlantDetail, error)
}
type PlantController struct{ reader CatalogReader }

func NewPlantController(reader CatalogReader) *PlantController {
	return &PlantController{reader: reader}
}

func validCursor(c *gin.Context) bool {
	if !repositories.ValidCursor(c.Query("cursor")) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cursor"})
		return false
	}
	return true
}
func fail(c *gin.Context, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database_unavailable"})
}
func (p *PlantController) List(c *gin.Context) {
	if !validCursor(c) {
		return
	}
	q := c.Query("q")
	if utf8.RuneCountInString(q) > 160 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query_too_long"})
		return
	}
	enriched := false
	if raw := c.Query("enriched"); raw != "" {
		var err error
		enriched, err = strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_enriched"})
			return
		}
	}
	page, err := p.reader.List(c.Request.Context(), q, c.Query("cursor"), enriched)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}
func (p *PlantController) Root(c *gin.Context) {
	root, err := p.reader.Root(c.Request.Context())
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, root)
}
func (p *PlantController) Children(c *gin.Context) {
	if !validCursor(c) {
		return
	}
	page, err := p.reader.Children(c.Request.Context(), c.Param("id"), c.Query("cursor"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}
func (p *PlantController) Detail(c *gin.Context) {
	detail, err := p.reader.Detail(c.Request.Context(), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (p *PlantController) Species(c *gin.Context) {
	detail, err := p.reader.Detail(c.Request.Context(), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	if detail.Taxon.Rank != "species" || detail.Taxon.Status != "accepted" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.JSON(http.StatusOK, detail)
}
