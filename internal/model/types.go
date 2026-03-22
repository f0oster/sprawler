package model

import "time"

// ErrorCategory classifies why an API call failed.
type ErrorCategory string

// Error categories for API call classification.
const (
	ErrThrottle    ErrorCategory = "throttle"     // 429
	ErrAuth        ErrorCategory = "auth"         // 401, 403
	ErrTimeout     ErrorCategory = "timeout"      // context.DeadlineExceeded
	ErrServerError ErrorCategory = "server_error" // 500, 502, 503, 504
	ErrNetwork     ErrorCategory = "network"      // connection errors
	ErrUnknown     ErrorCategory = "unknown"      // unclassified
)

// OperationFailure captures why a specific operation failed.
type OperationFailure struct {
	Category   ErrorCategory `json:"category"`
	HTTPStatus int           `json:"http_status"`
	Detail     string        `json:"detail"`
}

// SPSiteOutcome tracks what happened to each SharePoint site that didn't fully succeed.
type SPSiteOutcome struct {
	SiteURL   string        `json:"site_url"`
	SiteID    string        `json:"site_id"`
	Timestamp time.Time     `json:"timestamp"`
	Duration  time.Duration `json:"duration_ms"`
	// Operation results — nil means succeeded, non-nil means failed
	UsersFailure  *OperationFailure `json:"users_error,omitempty"`
	GroupsFailure *OperationFailure `json:"groups_error,omitempty"`
	// Counts for what DID succeed (useful for partial failures)
	UsersFetched   int `json:"users_fetched"`
	GroupsFetched  int `json:"groups_fetched"`
	MembersFetched int `json:"members_fetched"`
	// Partial failures within groups (individual member fetches that failed)
	MemberErrors        int `json:"member_errors"`
	MemberTimeoutErrors int `json:"member_timeout_errors"`
}

// ODSiteOutcome tracks what happened to each OneDrive site that didn't fully succeed.
type ODSiteOutcome struct {
	SiteURL     string        `json:"site_url"`
	SiteID      string        `json:"site_id"`
	UserAccount string        `json:"user_account"`
	Timestamp   time.Time     `json:"timestamp"`
	Duration    time.Duration `json:"duration_ms"`
	// Operation results
	UsersFailure   *OperationFailure `json:"users_error,omitempty"`
	ProfileFailure *OperationFailure `json:"profile_error,omitempty"`
	// Counts for what DID succeed
	UsersFetched   int  `json:"users_fetched"`
	ProfileFetched bool `json:"profile_fetched"`
}
