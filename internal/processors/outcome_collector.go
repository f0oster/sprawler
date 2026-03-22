package processors

import (
	"sync"

	"sprawler/internal/model"
)

// OutcomeCollector accumulates per-site outcome records from concurrent workers.
// It is safe for concurrent use.
type OutcomeCollector struct {
	spOutcomes []model.SPSiteOutcome
	odOutcomes []model.ODSiteOutcome
	mu         sync.Mutex
}

// RecordSPOutcome appends a SharePoint site outcome.
func (c *OutcomeCollector) RecordSPOutcome(outcome model.SPSiteOutcome) {
	c.mu.Lock()
	c.spOutcomes = append(c.spOutcomes, outcome)
	c.mu.Unlock()
}

// RecordODOutcome appends a OneDrive site outcome.
func (c *OutcomeCollector) RecordODOutcome(outcome model.ODSiteOutcome) {
	c.mu.Lock()
	c.odOutcomes = append(c.odOutcomes, outcome)
	c.mu.Unlock()
}

// SPOutcomes returns a copy of all recorded SharePoint outcomes.
func (c *OutcomeCollector) SPOutcomes() []model.SPSiteOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]model.SPSiteOutcome, len(c.spOutcomes))
	copy(result, c.spOutcomes)
	return result
}

// ODOutcomes returns a copy of all recorded OneDrive outcomes.
func (c *OutcomeCollector) ODOutcomes() []model.ODSiteOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]model.ODSiteOutcome, len(c.odOutcomes))
	copy(result, c.odOutcomes)
	return result
}

// FailureSummary returns counts grouped by operation and error category.
func (c *OutcomeCollector) FailureSummary() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	summary := make(map[string]int64)

	for _, o := range c.spOutcomes {
		if o.UsersFailure != nil {
			summary["users/"+string(o.UsersFailure.Category)]++
		}
		if o.GroupsFailure != nil {
			summary["groups/"+string(o.GroupsFailure.Category)]++
		}
		if o.MemberErrors > 0 {
			summary["members/error"] += int64(o.MemberErrors)
		}
		if o.MemberTimeoutErrors > 0 {
			summary["members/timeout"] += int64(o.MemberTimeoutErrors)
		}
	}

	for _, o := range c.odOutcomes {
		if o.UsersFailure != nil {
			summary["users/"+string(o.UsersFailure.Category)]++
		}
		if o.ProfileFailure != nil {
			summary["profile/"+string(o.ProfileFailure.Category)]++
		}
	}

	return summary
}
