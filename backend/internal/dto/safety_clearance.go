package dto

import "time"

// CreateSafetyClearance is the public write contract for 安全许可. Status is deliberately
// omitted so callers cannot bypass the service state machine. PlanCode and
// WindowCode bind the clearance to its interlock aggregates; the server
// re-reads and versions both of them on submit and release.
type CreateSafetyClearance struct {
	Code          string    `json:"code" binding:"required,min=2,max=64"`
	Name          string    `json:"name" binding:"required,min=2,max=160"`
	Description   string    `json:"description" binding:"max=1000"`
	Facility      string    `json:"facility" binding:"required,max=120"`
	Owner         string    `json:"owner" binding:"required,max=120"`
	Category      string    `json:"category" binding:"required,max=80"`
	RiskLevel     string    `json:"riskLevel" binding:"required,oneof=low medium high critical"`
	MetricValue   float64   `json:"metricValue"`
	MetricUnit    string    `json:"metricUnit" binding:"max=24"`
	EffectiveAt   time.Time `json:"effectiveAt" binding:"required"`
	Evidence      string    `json:"evidence" binding:"max=2000"`
	PlanCode      string    `json:"planCode" binding:"required,min=2,max=64"`
	WindowCode    string    `json:"windowCode" binding:"required,min=2,max=64"`
	RelatedCode   string    `json:"relatedCode" binding:"max=64"`
	WindowVersion uint      `json:"windowVersion" binding:"omitempty,min=1"`
}

type UpdateSafetyClearance struct {
	ExpectedVersion uint      `json:"expectedVersion" binding:"required"`
	Name            string    `json:"name" binding:"required,min=2,max=160"`
	Description     string    `json:"description" binding:"max=1000"`
	Facility        string    `json:"facility" binding:"required,max=120"`
	Owner           string    `json:"owner" binding:"required,max=120"`
	Category        string    `json:"category" binding:"required,max=80"`
	RiskLevel       string    `json:"riskLevel" binding:"required,oneof=low medium high critical"`
	MetricValue     float64   `json:"metricValue"`
	MetricUnit      string    `json:"metricUnit" binding:"max=24"`
	EffectiveAt     time.Time `json:"effectiveAt" binding:"required"`
	Evidence        string    `json:"evidence" binding:"max=2000"`
	PlanCode        string    `json:"planCode" binding:"required,min=2,max=64"`
	WindowCode      string    `json:"windowCode" binding:"required,min=2,max=64"`
	RelatedCode     string    `json:"relatedCode" binding:"max=64"`
	WindowVersion   uint      `json:"windowVersion" binding:"omitempty,min=1"`
}
