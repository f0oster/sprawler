package api

import (
	"testing"

	csom "sprawler/internal/api/csom"
)

func TestExtractTenantDomain(t *testing.T) {
	c := &Client{}

	t.Run("admin URL", func(t *testing.T) {
		got, err := c.extractTenantDomain("https://contoso-admin.sharepoint.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://contoso-my.sharepoint.com/" {
			t.Fatalf("got %q, want https://contoso-my.sharepoint.com/", got)
		}
	})

	t.Run("admin URL with trailing slash", func(t *testing.T) {
		got, err := c.extractTenantDomain("https://contoso-admin.sharepoint.com/")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://contoso-my.sharepoint.com/" {
			t.Fatalf("got %q, want https://contoso-my.sharepoint.com/", got)
		}
	})

	t.Run("non-admin URL", func(t *testing.T) {
		got, err := c.extractTenantDomain("https://contoso.sharepoint.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://contoso-my.sharepoint.com/" {
			t.Fatalf("got %q, want https://contoso-my.sharepoint.com/", got)
		}
	})

	t.Run("empty URL returns error", func(t *testing.T) {
		_, err := c.extractTenantDomain("")
		if err == nil {
			t.Fatal("expected error for empty URL")
		}
	})

	t.Run("no hostname returns error", func(t *testing.T) {
		_, err := c.extractTenantDomain("not-a-url")
		if err == nil {
			t.Fatal("expected error for URL with no hostname")
		}
	})
}

func TestConvertCSOMSiteToModel(t *testing.T) {
	client := &Client{}

	t.Run("valid dates convert to ISO8601", func(t *testing.T) {
		input := csom.SiteProperties{
			CreatedTime:             "/Date(2023,0,10,8,0,0,0)/",
			LastContentModifiedDate: "/Date(2024,5,1,12,0,0,0)/",
			LockState:               "",
			Owner:                   "user@contoso.com",
			SiteId:                  "/Guid(aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa)/",
			StorageUsage:            512000,
			Template:                "SPSPERS#10",
			Title:                   "User One",
			Url:                     "https://contoso-my.sharepoint.com/personal/user_contoso_com",
		}

		site := client.convertCSOMSiteToModel(input)

		if site.TimeCreated != "2023-01-10T08:00:00Z" {
			t.Fatalf("TimeCreated = %q, want ISO8601", site.TimeCreated)
		}
		if site.Modified != "2024-06-01T12:00:00Z" {
			t.Fatalf("Modified = %q, want ISO8601", site.Modified)
		}
		if site.LastActivityOn != "2024-06-01T12:00:00Z" {
			t.Fatalf("LastActivityOn = %q, want same as Modified", site.LastActivityOn)
		}
	})

	t.Run("invalid date falls back to raw string", func(t *testing.T) {
		input := csom.SiteProperties{
			CreatedTime:             "not a date",
			LastContentModifiedDate: "also not a date",
		}

		site := client.convertCSOMSiteToModel(input)

		if site.TimeCreated != "not a date" {
			t.Fatalf("TimeCreated = %q, want raw string preserved", site.TimeCreated)
		}
		if site.Modified != "also not a date" {
			t.Fatalf("Modified = %q, want raw string preserved", site.Modified)
		}
	})

	t.Run("SiteId cleaned from Guid wrapper", func(t *testing.T) {
		input := csom.SiteProperties{
			SiteId: "/Guid(bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb)/",
		}

		site := client.convertCSOMSiteToModel(input)

		if site.SiteId != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
			t.Fatalf("SiteId = %q, want cleaned GUID", site.SiteId)
		}
	})

	t.Run("field mapping", func(t *testing.T) {
		input := csom.SiteProperties{
			Owner:        "owner@contoso.com",
			Template:     "GROUP#0",
			Url:          "https://contoso.sharepoint.com/sites/eng",
			Title:        "Engineering",
			StorageUsage: 1048576,
			LockState:    "NoAccess",
		}

		site := client.convertCSOMSiteToModel(input)

		if site.CreatedByEmail != "owner@contoso.com" {
			t.Fatalf("CreatedByEmail = %q, want Owner value", site.CreatedByEmail)
		}
		if site.TemplateName != "GROUP#0" {
			t.Fatalf("TemplateName = %q, want Template value", site.TemplateName)
		}
		if site.SiteUrl != "https://contoso.sharepoint.com/sites/eng" {
			t.Fatalf("SiteUrl = %q, want Url value", site.SiteUrl)
		}
		if site.StorageUsed != 1048576.0 {
			t.Fatalf("StorageUsed = %f, want float64 of StorageUsage", site.StorageUsed)
		}
		if site.LockState != "NoAccess" {
			t.Fatalf("LockState = %q, want NoAccess", site.LockState)
		}
	})
}
