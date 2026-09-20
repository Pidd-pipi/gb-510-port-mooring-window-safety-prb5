package service

import "errors"

var (
	ErrInvalidTransition = errors.New("requested status transition is not allowed")
	ErrInvalidInput      = errors.New("business input validation failed")
	ErrUnauthorized      = errors.New("invalid username or password")
	ErrInactiveUser      = errors.New("user account is inactive")
	ErrSelfApproval      = errors.New("the submitter cannot approve the same safety clearance")
	ErrReviewerRequired  = errors.New("a reviewer or administrator must perform the second confirmation")
	ErrWindowVersion     = errors.New("the weather window version is missing or changed")

	// ErrInterlockBasisMissing means the clearance does not record a plan code
	// or window code, so no interlock can be evaluated.
	ErrInterlockBasisMissing = errors.New("the clearance must record both a mooring plan code and a weather window code")
	// ErrInterlockInvalid means the re-read plan/window no longer satisfy the
	// approval/safety/version conditions captured by the clearance.
	ErrInterlockInvalid = errors.New("the clearance interlock basis is no longer valid")
	// ErrReleasedBasisLocked prevents editing the bound basis of a clearance
	// whose historical release decision must stay reproducible.
	ErrReleasedBasisLocked = errors.New("a released clearance cannot be rebound to another plan or window")
)
