package processors

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"sprawler/internal/model"
)

var defaultSkipTemplates = buildTemplateSkipSet([]string{
	"TEAMCHANNEL#0", "TEAMCHANNEL#1", "APPCATALOG#0", "REDIRECTSITE#0",
})

func TestShouldSkipSite(t *testing.T) {
	tests := []struct {
		name string
		site model.Site
		want SkipReason
	}{
		{"team channel #0", model.Site{TemplateName: "TEAMCHANNEL#0"}, SkipReasonTemplate},
		{"team channel #1", model.Site{TemplateName: "TEAMCHANNEL#1"}, SkipReasonTemplate},
		{"app catalog", model.Site{TemplateName: "APPCATALOG#0"}, SkipReasonTemplate},
		{"redirect site", model.Site{TemplateName: "REDIRECTSITE#0"}, SkipReasonTemplate},
		{"tenant root", model.Site{SiteUrl: "https://contoso.sharepoint.com/"}, SkipReasonTenantRoot},
		{"normal site", model.Site{SiteUrl: "https://contoso.sharepoint.com/sites/hr", TemplateName: "STS#3"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldSkipSite(tt.site, defaultSkipTemplates))
		})
	}
}

func TestShouldSkipSite_CustomTemplates(t *testing.T) {
	custom := buildTemplateSkipSet([]string{"GROUP#0"})

	t.Run("custom template skipped", func(t *testing.T) {
		site := model.Site{TemplateName: "GROUP#0", SiteUrl: "https://contoso.sharepoint.com/sites/eng"}
		assert.Equal(t, SkipReasonTemplate, shouldSkipSite(site, custom))
	})

	t.Run("default template not skipped with custom set", func(t *testing.T) {
		site := model.Site{TemplateName: "TEAMCHANNEL#0", SiteUrl: "https://contoso.sharepoint.com/sites/chan"}
		assert.Equal(t, SkipReason(""), shouldSkipSite(site, custom))
	})
}

func TestShouldSkipSite_EmptyTemplates(t *testing.T) {
	empty := buildTemplateSkipSet(nil)

	site := model.Site{TemplateName: "TEAMCHANNEL#0", SiteUrl: "https://contoso.sharepoint.com/sites/chan"}
	assert.Equal(t, SkipReason(""), shouldSkipSite(site, empty))
}

func TestIsTenantRootSite(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"root with trailing slash", "https://contoso.sharepoint.com/", true},
		{"root without trailing slash", "https://contoso.sharepoint.com", true},
		{"subsite", "https://contoso.sharepoint.com/sites/hr", false},
		{"empty string", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTenantRootSite(tt.url))
		})
	}
}
