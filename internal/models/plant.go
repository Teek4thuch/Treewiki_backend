package models

import "time"

type Name struct {
	Name     string `json:"name"`
	Language string `json:"language"`
}

type Taxon struct {
	ID             string        `json:"id"`
	ParentID       *string       `json:"parent_id,omitempty"`
	ScientificName string        `json:"scientific_name"`
	Authorship     *string       `json:"authorship,omitempty"`
	Rank           string        `json:"rank"`
	Status         string        `json:"status"`
	CommonNames    []Name        `json:"common_names"`
	Descriptions   []Description `json:"descriptions,omitempty"`
	Images         []Image       `json:"images,omitempty"`
}

type Source struct {
	Title    string `json:"title"`
	DOI      string `json:"doi"`
	License  string `json:"license"`
	Citation string `json:"citation"`
}

type PlantDetail struct {
	Taxon          Taxon         `json:"taxon"`
	Classification []Taxon       `json:"classification"`
	Synonyms       []Taxon       `json:"synonyms"`
	Source         Source        `json:"source"`
	Descriptions   []Description `json:"descriptions"`
	Images         []Image       `json:"images"`
}

type Description struct {
	Language    string    `json:"language"`
	Content     string    `json:"content"`
	SourceURL   string    `json:"source_url"`
	RevisionID  int64     `json:"revision_id"`
	License     string    `json:"license"`
	Attribution string    `json:"attribution"`
	RetrievedAt time.Time `json:"retrieved_at"`
	MatchMethod string    `json:"match_method"`
}

type Image struct {
	ImageURL    string    `json:"image_url"`
	SourceURL   string    `json:"source_url"`
	Creator     string    `json:"creator"`
	License     string    `json:"license"`
	LicenseURL  string    `json:"license_url"`
	Attribution string    `json:"attribution"`
	RetrievedAt time.Time `json:"retrieved_at"`
	MatchMethod string    `json:"match_method"`
}

type TaxonPage struct {
	Taxa       []Taxon `json:"taxa"`
	NextCursor *string `json:"next_cursor,omitempty"`
}
