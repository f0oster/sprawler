package csom

import (
	"encoding/json"
	"fmt"
)

// ParseCSOMResponse parses a raw CSOM JSON response into structured site data.
func ParseCSOMResponse(jsomResp []byte) (*SPOSitePropertiesEnumerable, error) {
	var rawResponse []any
	if err := json.Unmarshal(jsomResp, &rawResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal CSOM response: %w", err)
	}

	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("empty CSOM response")
	}

	lastJson, err := json.Marshal(rawResponse[len(rawResponse)-1])
	if err != nil {
		return nil, fmt.Errorf("failed to marshal CSOM response data: %w", err)
	}
	rawResponse = nil // Help GC

	var siteData SPOSitePropertiesEnumerable
	if err := json.Unmarshal(lastJson, &siteData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal SitePropertiesEnumerable: %w", err)
	}

	return &siteData, nil
}
