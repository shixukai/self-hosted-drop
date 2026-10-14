package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/shixukai/self-hosted-drop/internal/archive"
	"net/http"
	"time"
)

type Site struct {
	ID                  string     `json:"id"`
	Slug                string     `json:"slug"`
	Name                string     `json:"name"`
	Description         string     `json:"description"`
	Status              string     `json:"status"`
	DesiredStatus       string     `json:"desiredStatus"`
	Generation          int64      `json:"generation"`
	AppliedGeneration   int64      `json:"appliedGeneration"`
	DeploymentID        *string    `json:"appliedDeploymentId"`
	DesiredDeploymentID *string    `json:"desiredDeploymentId"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	DeletedAt           *time.Time `json:"deletedAt"`
	URL                 string     `json:"url"`
	DeploymentCount     int        `json:"deploymentCount"`
	Bytes               int64      `json:"bytes"`
}
type Deployment struct {
	ID                      string           `json:"id"`
	SiteID                  string           `json:"siteId"`
	Status                  string           `json:"status"`
	ArtifactState           string           `json:"artifactState"`
	InputHash               string           `json:"inputHash"`
	ArtifactHash            string           `json:"artifactHash"`
	Filename                string           `json:"filename"`
	Metadata                archive.Metadata `json:"metadata"`
	Report                  archive.Result   `json:"report"`
	Files                   []archive.File   `json:"files"`
	ReceivedBytes           int64            `json:"receivedBytes"`
	ExpandedBytes           int64            `json:"expandedBytes"`
	FileCount               int              `json:"fileCount"`
	ErrorCode               string           `json:"errorCode"`
	ErrorMessage            string           `json:"errorMessage"`
	AutoPublicationID       *string          `json:"autoPublicationId"`
	AutoPublishErrorCode    string           `json:"autoPublishErrorCode"`
	AutoPublishErrorMessage string           `json:"autoPublishErrorMessage"`
	CreatedBy               string           `json:"createdBy"`
	Pinned                  bool             `json:"pinned"`
	CreatedAt               time.Time        `json:"createdAt"`
	UpdatedAt               time.Time        `json:"updatedAt"`
}
type Publication struct {
	ID                 string     `json:"id"`
	SiteID             string     `json:"siteId"`
	DeploymentID       *string    `json:"deploymentId"`
	Action             string     `json:"action"`
	ExpectedGeneration int64      `json:"expectedGeneration"`
	TargetGeneration   int64      `json:"targetGeneration"`
	TargetStatus       string     `json:"targetStatus"`
	Status             string     `json:"status"`
	ErrorCode          string     `json:"errorCode"`
	ErrorMessage       string     `json:"errorMessage"`
	CreatedAt          time.Time  `json:"createdAt"`
	AppliedAt          *time.Time `json:"appliedAt"`
}
type APIError struct {
	Status    int    `json:"-"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
	Retryable bool   `json:"retryable"`
	Details   any    `json:"details,omitempty"`
}

func (e *APIError) Error() string { return e.Code }
func apiError(status int, code, msg string) *APIError {
	return &APIError{Status: status, Code: code, Message: msg}
}
func id() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func token() string          { return id() + id() }
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}
func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type scanner interface{ Scan(...any) error }

const siteFields = `id,slug,name,description,status,desired_status,generation,applied_generation,applied_deployment_id,desired_deployment_id,created_at,updated_at,deleted_at`

func scanSite(row scanner) (Site, error) {
	var s Site
	e := row.Scan(&s.ID, &s.Slug, &s.Name, &s.Description, &s.Status, &s.DesiredStatus, &s.Generation, &s.AppliedGeneration, &s.DeploymentID, &s.DesiredDeploymentID, &s.CreatedAt, &s.UpdatedAt, &s.DeletedAt)
	return s, e
}

const deploymentFields = `id,site_id,status,artifact_state,input_hash,artifact_hash,filename,metadata,report,files,received_bytes,expanded_bytes,file_count,error_code,error_message,created_by,pinned,created_at,updated_at,auto_publication_id,auto_publish_error_code,auto_publish_error_message`

func scanDeployment(row scanner) (Deployment, error) {
	var d Deployment
	var m, r, f []byte
	e := row.Scan(&d.ID, &d.SiteID, &d.Status, &d.ArtifactState, &d.InputHash, &d.ArtifactHash, &d.Filename, &m, &r, &f, &d.ReceivedBytes, &d.ExpandedBytes, &d.FileCount, &d.ErrorCode, &d.ErrorMessage, &d.CreatedBy, &d.Pinned, &d.CreatedAt, &d.UpdatedAt, &d.AutoPublicationID, &d.AutoPublishErrorCode, &d.AutoPublishErrorMessage)
	if e != nil {
		return d, e
	}
	if e = json.Unmarshal(m, &d.Metadata); e != nil {
		return d, e
	}
	if e = json.Unmarshal(r, &d.Report); e != nil {
		return d, e
	}
	e = json.Unmarshal(f, &d.Files)
	return d, e
}

const publicationFields = `id,site_id,deployment_id,action,expected_generation,target_generation,target_status,status,error_code,error_message,created_at,applied_at`

func scanPublication(row scanner) (Publication, error) {
	var p Publication
	e := row.Scan(&p.ID, &p.SiteID, &p.DeploymentID, &p.Action, &p.ExpectedGeneration, &p.TargetGeneration, &p.TargetStatus, &p.Status, &p.ErrorCode, &p.ErrorMessage, &p.CreatedAt, &p.AppliedAt)
	return p, e
}
