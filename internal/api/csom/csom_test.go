package csom

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCSOMDate(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{
			"valid date",
			"/Date(2024,0,15,10,30,0,0)/",
			time.Date(2024, time.January, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			"december boundary with ms",
			"/Date(2024,11,31,23,59,59,999)/",
			time.Date(2024, time.December, 31, 23, 59, 59, 999_000_000, time.UTC),
		},
		{
			"embedded in text",
			`"CreatedTime":"/Date(2024,0,15,10,30,0,0)/"`,
			time.Date(2024, time.January, 15, 10, 30, 0, 0, time.UTC),
		},
		{"empty string", "", time.Time{}},
		{"missing fields", "/Date(2024,0,15)/", time.Time{}},
		{"non-numeric", "/Date(abc,0,15,10,30,0,0)/", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCSOMDate(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCleanSiteId(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"wrapped", "/Guid(abc-123)/", "abc-123"},
		{"already clean", "abc-123", "abc-123"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CleanSiteId(tt.input))
		})
	}
}

func TestParseCSOMResponse(t *testing.T) {
	t.Run("valid response", func(t *testing.T) {
		input := `[
			{"_ObjectType_":"Some.Metadata"},
			{
				"NextStartIndexFromSharePoint": "abc123",
				"_Child_Items_": [
					{"Url": "https://contoso.sharepoint.com/sites/hr", "Title": "HR"}
				]
			}
		]`
		result, err := ParseCSOMResponse([]byte(input))
		require.NoError(t, err)
		assert.Equal(t, "abc123", result.NextStartIndexFromSharePoint)
		assert.Len(t, result.ChildItems, 1)
		assert.Equal(t, "HR", result.ChildItems[0].Title)
	})

	t.Run("single element", func(t *testing.T) {
		input := `[{"NextStartIndexFromSharePoint": "", "_Child_Items_": []}]`
		result, err := ParseCSOMResponse([]byte(input))
		require.NoError(t, err)
		assert.Empty(t, result.NextStartIndexFromSharePoint)
		assert.Empty(t, result.ChildItems)
	})

	t.Run("empty array", func(t *testing.T) {
		_, err := ParseCSOMResponse([]byte(`[]`))
		assert.Error(t, err)
	})

	t.Run("invalid json", func(t *testing.T) {
		_, err := ParseCSOMResponse([]byte(`not json`))
		assert.Error(t, err)
	})
}

func TestBuildOneDriveQuery(t *testing.T) {
	xml, err := BuildOneDriveQuery("testfilter", "idx42")
	require.NoError(t, err)
	assert.NotEmpty(t, xml)
	assert.True(t, strings.Contains(xml, "testfilter"), "filter value should appear in output")
	assert.True(t, strings.Contains(xml, "idx42"), "startIndex should appear in output")
}
