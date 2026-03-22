package csom

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// csomDateRegex matches the CSOM date format: /Date(year,month,day,hour,min,sec,ms)/.
var csomDateRegex = regexp.MustCompile(`/Date\((\d+),(\d+),(\d+),(\d+),(\d+),(\d+),(\d+)\)/`)

// ParseCSOMDate parses a SharePoint CSOM date string into a time.Time.
// Returns the zero time if the input is malformed.
func ParseCSOMDate(input string) time.Time {
	matches := csomDateRegex.FindStringSubmatch(input)
	if len(matches) != 8 {
		return time.Time{}
	}

	values := make([]int, 7)
	for i := 0; i < 7; i++ {
		n, err := strconv.Atoi(matches[i+1])
		if err != nil {
			return time.Time{}
		}
		values[i] = n
	}

	return time.Date(
		values[0], time.Month(values[1]+1), values[2],
		values[3], values[4], values[5], values[6]*1_000_000,
		time.UTC,
	)
}

// CleanSiteId strips the "/Guid(...)/" wrapper from a SharePoint site ID.
func CleanSiteId(siteId string) string {
	clean := strings.TrimPrefix(siteId, "/Guid(")
	clean = strings.TrimSuffix(clean, ")/")
	return clean
}
