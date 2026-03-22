package processors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sprawler/internal/api"
	"sprawler/internal/model"
)

// mockAPIClient implements APIClient for testing processors without gosip.
type mockAPIClient struct {
	// SP site enumeration
	sites []model.Site

	// Per-site users keyed by SiteUrl
	siteUsers map[string][]model.SiteUser

	// Per-site group results keyed by SiteUrl
	groupResults map[string]*api.GroupFetchResult

	// OD personal sites
	personalSites []model.Site

	// Profiles keyed by account name (claims format)
	profiles map[string]*model.UserProfile

	// Error simulation
	errorSiteUsers map[string]error
	errorProfiles  map[string]error
}

func (m *mockAPIClient) GetSites(ctx context.Context, sites chan<- model.Site, pageSize int, maxPages int, onPage func(int)) error {
	if onPage != nil {
		onPage(len(m.sites))
	}
	for _, s := range m.sites {
		select {
		case sites <- s:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (m *mockAPIClient) GetSiteUsers(ctx context.Context, site model.Site) ([]model.SiteUser, error) {
	if err, ok := m.errorSiteUsers[site.SiteUrl]; ok {
		return nil, err
	}
	users := m.siteUsers[site.SiteUrl]
	result := make([]model.SiteUser, len(users))
	for i, u := range users {
		u.SiteId = site.SiteId
		result[i] = u
	}
	return result, nil
}

func (m *mockAPIClient) GetSiteGroups(ctx context.Context, site model.Site, memberTimeout time.Duration) (*api.GroupFetchResult, error) {
	r, ok := m.groupResults[site.SiteUrl]
	if !ok {
		return &api.GroupFetchResult{}, nil
	}
	result := &api.GroupFetchResult{
		MemberErrors:        r.MemberErrors,
		MemberTimeoutErrors: r.MemberTimeoutErrors,
	}
	for _, g := range r.Groups {
		g.SiteId = site.SiteId
		result.Groups = append(result.Groups, g)
	}
	for _, mb := range r.Members {
		mb.SiteId = site.SiteId
		result.Members = append(result.Members, mb)
	}
	return result, nil
}

func (m *mockAPIClient) GetPersonalSites(ctx context.Context, sites chan<- model.Site, maxSites int, onPage func(int)) error {
	toSend := m.personalSites
	if maxSites > 0 && len(toSend) > maxSites {
		toSend = toSend[:maxSites]
	}
	if onPage != nil {
		onPage(len(toSend))
	}
	for _, s := range toSend {
		select {
		case sites <- s:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (m *mockAPIClient) GetUserProfile(ctx context.Context, userID string) (*model.UserProfile, error) {
	if err, ok := m.errorProfiles[userID]; ok {
		return nil, err
	}
	if p, ok := m.profiles[userID]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, fmt.Errorf("profile not found: %s", userID)
}

func (m *mockAPIClient) GetMetrics() model.APIMetrics          { return model.APIMetrics{} }
func (m *mockAPIClient) GetTransportStats() api.TransportStats { return api.TransportStats{} }
func (m *mockAPIClient) GetThrottlingCount() int64             { return 0 }
func (m *mockAPIClient) GetNetworkErrorCount() int64           { return 0 }
func (m *mockAPIClient) HealthCheck(ctx context.Context) error { return nil }

// --- Fixture loading helpers ---

// loadFixture reads a raw JSON fixture file.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("failed to load fixture %s: %v", name, err)
	}
	return data
}

// gosipResults is the common SharePoint REST API wrapper.
type gosipResults[T any] struct {
	D struct {
		Results []T `json:"results"`
	} `json:"d"`
}

// loadSitesFixture loads sites from a REST API response fixture.
func loadSitesFixture(t *testing.T, name string) []model.Site {
	t.Helper()
	raw := loadFixture(t, name)
	var w gosipResults[model.Site]
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("parse sites fixture %s: %v", name, err)
	}
	return w.D.Results
}

// loadPersonalSitesFixture loads pre-converted model.Site objects from a JSON array.
func loadPersonalSitesFixture(t *testing.T, name string) []model.Site {
	t.Helper()
	raw := loadFixture(t, name)
	var sites []model.Site
	if err := json.Unmarshal(raw, &sites); err != nil {
		t.Fatalf("parse personal sites fixture %s: %v", name, err)
	}
	return sites
}

// loadUsersFixture loads per-site users from a keyed gosip REST fixture.
func loadUsersFixture(t *testing.T, name string) map[string][]model.SiteUser {
	t.Helper()
	return loadKeyedGosipFixture[model.SiteUser](t, name)
}

// loadGroupsFixture loads per-site groups from a keyed gosip REST fixture.
func loadGroupsFixture(t *testing.T, name string) map[string][]model.SiteGroup {
	t.Helper()
	return loadKeyedGosipFixture[model.SiteGroup](t, name)
}

// loadMembersFixture loads per-group members from a keyed gosip REST fixture.
func loadMembersFixture(t *testing.T, name string) map[string][]model.GroupMember {
	t.Helper()
	return loadKeyedGosipFixture[model.GroupMember](t, name)
}

// loadKeyedGosipFixture loads a keyed fixture where each value is {"d":{"results":[...]}}.
func loadKeyedGosipFixture[T any](t *testing.T, name string) map[string][]T {
	t.Helper()
	raw := loadFixture(t, name)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse keyed fixture %s: %v", name, err)
	}
	result := make(map[string][]T, len(m))
	for k, v := range m {
		var w gosipResults[T]
		if err := json.Unmarshal(v, &w); err != nil {
			t.Fatalf("parse keyed fixture %s key %q: %v", name, k, err)
		}
		result[k] = w.D.Results
	}
	return result
}

// buildGroupResults combines group and member fixtures into per-site GroupFetchResults.
func buildGroupResults(t *testing.T, groupsFile, membersFile string) map[string]*api.GroupFetchResult {
	t.Helper()
	groups := loadGroupsFixture(t, groupsFile)
	members := loadMembersFixture(t, membersFile)

	results := make(map[string]*api.GroupFetchResult, len(groups))
	for siteURL, siteGroups := range groups {
		r := &api.GroupFetchResult{Groups: siteGroups}
		for _, g := range siteGroups {
			key := fmt.Sprintf("%s|%d", siteURL, g.ID)
			for _, mb := range members[key] {
				mb.GroupId = g.ID
				r.Members = append(r.Members, mb)
			}
		}
		results[siteURL] = r
	}
	return results
}

// loadProfilesFixture parses the gosip profile format into model.UserProfile objects.
func loadProfilesFixture(t *testing.T, name string) map[string]*model.UserProfile {
	t.Helper()
	raw := loadFixture(t, name)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse profiles fixture %s: %v", name, err)
	}

	result := make(map[string]*model.UserProfile, len(m))
	for accountName, v := range m {
		var w struct {
			D struct {
				PersonalUrl           string `json:"PersonalUrl"`
				UserProfileProperties struct {
					Results []struct {
						Key   string `json:"Key"`
						Value string `json:"Value"`
					} `json:"results"`
				} `json:"UserProfileProperties"`
			} `json:"d"`
		}
		if err := json.Unmarshal(v, &w); err != nil {
			t.Fatalf("parse profile %s: %v", accountName, err)
		}

		props := make(map[string]string)
		for _, p := range w.D.UserProfileProperties.Results {
			props[p.Key] = p.Value
		}

		sid := props["SID"]
		sid = strings.TrimPrefix(sid, "i:0h.f|membership|")
		sid = strings.TrimSuffix(sid, "@live.com")

		result[accountName] = &model.UserProfile{
			AadObjectId:                    props["msOnline-ObjectId"],
			AccountName:                    accountName,
			PersonalUrl:                    w.D.PersonalUrl,
			ProfileSid:                     sid,
			PersonalSiteInstantiationState: props["SPS-PersonalSiteInstantiationState"],
			UserPrincipalName:              strings.Replace(accountName, "i:0#.f|membership|", "", 1),
		}
	}
	return result
}
