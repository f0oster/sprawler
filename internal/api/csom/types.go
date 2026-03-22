// Package csom provides CSOM XML query building and response parsing for OneDrive site enumeration.
package csom

// SPOSitePropertiesEnumerable represents the paginated CSOM response for site collections.
type SPOSitePropertiesEnumerable struct {
	NextStartIndexFromSharePoint string           `json:"NextStartIndexFromSharePoint"`
	ChildItems                   []SiteProperties `json:"_Child_Items_"`
}

// SiteProperties holds site metadata fields returned by CSOM queries.
type SiteProperties struct {
	CreatedTime             string `json:"CreatedTime"`             // Site creation timestamp
	LastContentModifiedDate string `json:"LastContentModifiedDate"` // Last modification timestamp
	LockState               string `json:"LockState"`               // Site lock state
	Owner                   string `json:"Owner"`                   // Site owner email
	SiteId                  string `json:"SiteId"`                  // Unique site identifier
	StorageUsage            int64  `json:"StorageUsage"`            // Storage usage in bytes
	Template                string `json:"Template"`                // Site template (e.g., SPSPERS)
	Title                   string `json:"Title"`                   // Site display title
	Url                     string `json:"Url"`                     // Site URL
}
