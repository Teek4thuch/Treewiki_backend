package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"treewiki/wiki_backend/internal/models"
)

type fakeCatalog struct {
	query, cursor, parent string
	enriched              bool
	err                   error
}

func (f *fakeCatalog) List(_ context.Context, q, cursor string, enriched bool) (models.TaxonPage, error) {
	f.query, f.cursor = q, cursor
	f.enriched = enriched
	return models.TaxonPage{Taxa: []models.Taxon{{ID: "plant:1", ScientificName: "Mangifera indica", Rank: "species", Status: "accepted", CommonNames: []models.Name{}}}}, f.err
}
func (f *fakeCatalog) Children(_ context.Context, parent, cursor string) (models.TaxonPage, error) {
	f.parent = parent
	return models.TaxonPage{Taxa: []models.Taxon{}}, f.err
}
func (f *fakeCatalog) Root(context.Context) (models.Taxon, error) {
	return models.Taxon{ID: "root", ScientificName: "Plantae"}, f.err
}
func (f *fakeCatalog) Detail(context.Context, string) (models.PlantDetail, error) {
	return models.PlantDetail{Taxon: models.Taxon{ID: "plant:1", Rank: "species", Status: "accepted"}}, f.err
}

func TestCatalogRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeCatalog{}
	controller := NewPlantController(fake)
	router := gin.New()
	router.GET("/plants", controller.List)
	router.GET("/taxa/:id/children", controller.Children)
	router.GET("/plants/:id", controller.Species)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/plants?q=Mangifera", nil))
	if response.Code != http.StatusOK || fake.query != "Mangifera" || !strings.Contains(response.Body.String(), `"id":"plant:1"`) {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/plants?enriched=true", nil))
	if response.Code != http.StatusOK || !fake.enriched {
		t.Fatalf("enriched flag: %d", response.Code)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/plants?enriched=invalid", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid enriched flag: %d", response.Code)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/plants?cursor=bad!", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor: %d", response.Code)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/taxa/root/children", nil))
	if response.Code != http.StatusOK || fake.parent != "root" {
		t.Fatalf("children: %d %s", response.Code, response.Body.String())
	}
	fake.err = pgx.ErrNoRows
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/plants/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", response.Code)
	}
	fake.err = errors.New("private details")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/plants", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private details") {
		t.Fatalf("failure: %d %s", response.Code, response.Body.String())
	}
}
