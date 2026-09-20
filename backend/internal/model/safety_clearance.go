package model

import "time"

// SafetyClearance models 安全许可 as an independently versioned aggregate. The
// clearance binds a 系泊方案 (PlanCode) and a 风浪窗口 (WindowCode) together with
// the versions that were current when the first person submitted. Every submit
// or release re-reads both aggregates inside one transaction so the two-person
// confirmation can only proceed when the plan is approved, the window is safe
// and neither aggregate has changed since the recorded basis.
type SafetyClearance struct {
	BaseModel
	Facility      string     `json:"facility" gorm:"size:120;index"`
	Owner         string     `json:"owner" gorm:"size:120;index"`
	Category      string     `json:"category" gorm:"size:80;index"`
	RiskLevel     string     `json:"riskLevel" gorm:"size:32;index"`
	MetricValue   float64    `json:"metricValue"`
	MetricUnit    string     `json:"metricUnit" gorm:"size:24"`
	EffectiveAt   time.Time  `json:"effectiveAt"`
	Evidence      string     `json:"evidence" gorm:"size:2000"`
	PlanCode      string     `json:"planCode" gorm:"size:64;index"`
	WindowCode    string     `json:"windowCode" gorm:"size:64;index"`
	RelatedCode   string     `json:"relatedCode" gorm:"size:64;index"`
	PlanVersion   uint       `json:"planVersion" gorm:"not null;default:0"`
	WindowVersion uint       `json:"windowVersion" gorm:"not null;default:1"`
	SubmittedBy   string     `json:"submittedBy" gorm:"size:80;index"`
	SubmittedAt   *time.Time `json:"submittedAt"`
	ConfirmedBy   string     `json:"confirmedBy" gorm:"size:80;index"`
	ConfirmedAt   *time.Time `json:"confirmedAt"`

	// InterlockBasisValid / InterlockInvalidReason are computed on every read by
	// re-reading the bound plan and window; they are never persisted. Cleared or
	// otherwise historical clearances stay valid because their released basis is
	// frozen in the audit trail.
	InterlockBasisValid    bool   `json:"interlockBasisValid" gorm:"-"`
	InterlockInvalidReason string `json:"interlockInvalidReason" gorm:"-"`
	PlanStatus             string `json:"planStatus" gorm:"-"`
	CurrentPlanVersion     uint   `json:"currentPlanVersion" gorm:"-"`
	WindowStatus           string `json:"windowStatus" gorm:"-"`
	CurrentWindowVersion   uint   `json:"currentWindowVersion" gorm:"-"`
}

func (item *SafetyClearance) GetBase() *BaseModel { return &item.BaseModel }

func (item SafetyClearance) TableName() string { return "safety_clearances" }

var SafetyClearanceInitialStatus = "pending"
