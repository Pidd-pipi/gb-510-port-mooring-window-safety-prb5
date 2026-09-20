package model

import "time"

// SafetyClearance models 安全许可 as an independently versioned aggregate. The fields
// cover ownership, operational context, evidence and measured risk so later
// changes naturally span persistence, service and UI layers. PlanCode and
// WindowCode bind the clearance to the plan/window pair that the interlock
// re-reads on every submit or release.
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
	RelatedCode   string     `json:"relatedCode" gorm:"size:64;index"`
	PlanCode      string     `json:"planCode" gorm:"size:64;index"`
	WindowCode    string     `json:"windowCode" gorm:"size:64;index"`
	WindowVersion uint       `json:"windowVersion" gorm:"not null;default:1"`
	SubmittedBy   string     `json:"submittedBy" gorm:"size:80;index"`
	SubmittedAt   *time.Time `json:"submittedAt"`
	ConfirmedBy   string     `json:"confirmedBy" gorm:"size:80;index"`
	ConfirmedAt   *time.Time `json:"confirmedAt"`
	// Interlock is computed on read from the live plan/window state and is
	// never persisted, so a page refresh always re-reads the current basis.
	Interlock *ClearanceInterlock `json:"interlock,omitempty" gorm:"-"`
}

// ClearanceInterlock captures the live plan/window basis behind a safety
// clearance. InvalidReason is only populated for pending clearances; released
// history keeps the basis snapshot but is never invalidated retroactively.
type ClearanceInterlock struct {
	PlanCode              string `json:"planCode"`
	PlanStatus            string `json:"planStatus"`
	PlanApproved          bool   `json:"planApproved"`
	WindowCode            string `json:"windowCode"`
	WindowStatus          string `json:"windowStatus"`
	WindowSafe            bool   `json:"windowSafe"`
	ExpectedWindowVersion uint   `json:"expectedWindowVersion"`
	CurrentWindowVersion  uint   `json:"currentWindowVersion"`
	WindowVersionMatch    bool   `json:"windowVersionMatch"`
	Satisfied             bool   `json:"satisfied"`
	InvalidReason         string `json:"invalidReason"`
}

func (item *SafetyClearance) GetBase() *BaseModel { return &item.BaseModel }

func (item SafetyClearance) TableName() string { return "safety_clearances" }

var SafetyClearanceInitialStatus = "pending"
