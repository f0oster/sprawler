// Package model defines data structures for SharePoint and OneDrive entities.
// Contains models for sites, users, groups, profiles, and run status tracking.
package model

// RunStatus tracks the state of a data export run.
type RunStatus struct {
	ID                  uint `json:"id"`
	FullExportCompleted bool `json:"FullExportCompleted"`
}

// UserProfile represents a SharePoint user profile from the User Profile Service.
//
// Size: ~341 B avg (144 B fixed + ~197 B heap, sampled)
//
//	Field                          Type     Fixed   Avg heap   Max heap   Notes
//	PersonalUrl                    string   16 B    72 B       112 B
//	DocId                          string   16 B     0 B         0 B      100% NULL in prod
//	AadObjectId                    string   16 B    36 B        36 B
//	AccountName                    string   16 B    45 B        85 B
//	PersonalSiteInstantiationState string   16 B     1 B         4 B
//	ProfileSid                     string   16 B    16 B        16 B
//	UserProfileGuid                string   16 B     0 B         0 B      100% NULL in prod
//	UserPrincipalName              string   16 B    27 B        67 B
//	LastModifiedTime               string   16 B     0 B         0 B      100% NULL in prod
type UserProfile struct {
	PersonalUrl                    string `json:"personal_url"`
	DocId                          string `json:"doc_id"`
	AadObjectId                    string `json:"aad_object_id"`
	AccountName                    string `json:"account_name"`
	PersonalSiteInstantiationState string `json:"personal_site_instantiation_state"`
	ProfileSid                     string `json:"profile_sid"`
	UserProfileGuid                string `json:"user_profile_guid"`
	UserPrincipalName              string `json:"user_principal_name"`
	LastModifiedTime               string `json:"last_modified_time"`
}

// Site represents a SharePoint site collection.
//
// Size: ~438 B avg (176 B fixed + ~262 B heap, sampled)
//
//	Field            Type      Fixed   Avg heap   Max heap
//	SiteUrl          string    16 B    66 B       123 B
//	TimeCreated      string    16 B    20 B        20 B
//	Modified         string    16 B    20 B        20 B
//	Title            string    16 B    17 B       255 B
//	TemplateName     string    16 B     9 B        20 B
//	CreatedByEmail   string    16 B    32 B        90 B
//	GroupId          string    16 B    36 B        36 B
//	SiteId           string    16 B    36 B        36 B
//	LastActivityOn   string    16 B    20 B        20 B
//	StorageUsed      float64    8 B     —           —
//	LockState        string    16 B     6 B         8 B
type Site struct {
	SiteUrl        string  `json:"SiteUrl"`
	TimeCreated    string  `json:"TimeCreated"`
	Modified       string  `json:"Modified"`
	Title          string  `json:"Title"`
	TemplateName   string  `json:"TemplateName"`
	CreatedByEmail string  `json:"CreatedByEmail"`
	GroupId        string  `json:"GroupId"`
	SiteId         string  `json:"SiteId"`
	LastActivityOn string  `json:"LastActivityOn"`
	StorageUsed    float64 `json:"StorageUsed"`
	LockState      string  `json:"LockState"`
}

// UserId holds the identity claim components for a SharePoint user.
type UserId struct {
	NameId       string `json:"NameId"`
	NameIdIssuer string `json:"NameIdIssuer"`
}

// SiteUser represents a user associated with a specific SharePoint site.
//
// Size: ~370 B avg (184 B fixed + ~186 B heap, sampled)
//
//	Field                           Type     Fixed   Avg heap   Max heap   Notes
//	Email                           string   16 B    19 B        99 B      24% empty
//	Expiration                      string   16 B     0 B         0 B      100% NULL in prod
//	ID                              int       8 B     —           —
//	IsEmailAuthenticationGuestUser  bool      1 B     —           —
//	IsHiddenInUI                    bool      1 B     —           —
//	IsShareByEmailGuestUser         bool      1 B     —           —
//	IsSiteAdmin                     bool      1 B     —           —
//	LoginName                       string   16 B    46 B       108 B
//	PrincipalType                   int       8 B     —           —
//	Title                           string   16 B    17 B       255 B
//	UserId.NameId                   string   16 B    18 B        77 B      (embedded)
//	UserId.NameIdIssuer             string   16 B    31 B        52 B      (embedded)
//	UserPrincipalName               *string   8 B    19 B        90 B      27% NULL
//	SiteId                          string   16 B    36 B        36 B
//	Updated                         string   16 B     0 B         0 B      100% NULL in prod
type SiteUser struct {
	Email                          string  `json:"Email"`
	Expiration                     string  `json:"Expiration"`
	ID                             int     `json:"Id"`
	IsEmailAuthenticationGuestUser bool    `json:"IsEmailAuthenticationGuestUser"`
	IsHiddenInUI                   bool    `json:"IsHiddenInUI"`
	IsShareByEmailGuestUser        bool    `json:"IsShareByEmailGuestUser"`
	IsSiteAdmin                    bool    `json:"IsSiteAdmin"`
	LoginName                      string  `json:"LoginName"`
	PrincipalType                  int     `json:"PrincipalType"`
	Title                          string  `json:"Title"`
	UserId                         UserId  `json:"UserId"`
	UserPrincipalName              *string `json:"UserPrincipalName"`
	SiteId                         string  `json:"SiteId"`
	Updated                        string  `json:"Updated"`
}

// SiteGroup represents a SharePoint permission group on a site.
//
// Size: 120 B fixed (heap TODO: needs production sample)
//
//	Field         Type     Fixed
//	ID            int       8 B
//	Title         string   16 B
//	LoginName     string   16 B
//	Description   string   16 B
//	OwnerTitle    string   16 B
//	SiteId        string   16 B
//	Updated       string   16 B
type SiteGroup struct {
	ID          int    `json:"Id"`
	Title       string `json:"Title"`
	LoginName   string `json:"LoginName"`
	Description string `json:"Description"`
	OwnerTitle  string `json:"OwnerTitle"`
	SiteId      string `json:"SiteId"`
	Updated     string `json:"Updated"`
}

// GroupMember represents a member of a SharePoint site group.
//
// Size: 128 B fixed (heap TODO: needs production sample)
//
//	Field              Type      Fixed
//	ID                 int        8 B
//	Title              string    16 B
//	LoginName          string    16 B
//	Email              string    16 B
//	PrincipalType      int        8 B
//	UserPrincipalName  *string    8 B
//	GroupId            int        8 B
//	SiteId             string    16 B
//	Updated            string    16 B
type GroupMember struct {
	ID                int     `json:"Id"`
	Title             string  `json:"Title"`
	LoginName         string  `json:"LoginName"`
	Email             string  `json:"Email"`
	PrincipalType     int     `json:"PrincipalType"`
	UserPrincipalName *string `json:"UserPrincipalName"`
	GroupId           int     `json:"GroupId"`
	SiteId            string  `json:"SiteId"`
	Updated           string  `json:"Updated"`
}
