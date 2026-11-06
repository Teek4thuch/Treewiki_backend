package models

type HealthResponse struct {
	Status   string `json:"status"`
	Service  string `json:"service,omitempty"`
	Database string `json:"database,omitempty"`
}
