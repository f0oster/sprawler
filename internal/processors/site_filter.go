package processors

import (
	"net/url"
	"strings"

	"sprawler/internal/model"
)

// SkipReason represents why a site should be skipped during enumeration.
type SkipReason string

// Site skip reasons.
const (
	SkipReasonTemplate   SkipReason = "skip_template"
	SkipReasonTenantRoot SkipReason = "skip_tenant_root"
)

// buildTemplateSkipSet builds a set of templates to skip from a config slice.
func buildTemplateSkipSet(templates []string) map[string]struct{} {
	set := make(map[string]struct{}, len(templates))
	for _, t := range templates {
		set[t] = struct{}{}
	}
	return set
}

// shouldSkipSite determines if a site should be skipped during enumeration.
//
// Returns skip reason or empty string if site should be processed.
// Filters by template type and tenant root site detection.
func shouldSkipSite(site model.Site, skipTemplates map[string]struct{}) SkipReason {
	if _, skip := skipTemplates[site.TemplateName]; skip {
		return SkipReasonTemplate
	}

	if isTenantRootSite(site.SiteUrl) {
		return SkipReasonTenantRoot
	}

	return "" // Site is processable
}

// isTenantRootSite checks if URL represents a tenant root site.
//
// Tenant root sites have empty path or just "/" (e.g., https://tenant.sharepoint.com/).
func isTenantRootSite(siteURL string) bool {
	if siteURL == "" {
		return false
	}

	parsedURL, err := url.Parse(siteURL)
	if err != nil {
		return false
	}

	path := strings.TrimSpace(parsedURL.Path)
	return path == "" || path == "/"
}
