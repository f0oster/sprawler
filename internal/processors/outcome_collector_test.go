package processors

import (
	"testing"

	"sprawler/internal/model"
)

func TestOutcomeCollector_SPOutcomes(t *testing.T) {
	oc := &OutcomeCollector{}

	oc.RecordSPOutcome(model.SPSiteOutcome{
		SiteURL:      "https://a.com",
		UsersFailure: &model.OperationFailure{Category: model.ErrThrottle, HTTPStatus: 429, Detail: "429 Too Many Requests"},
	})
	oc.RecordSPOutcome(model.SPSiteOutcome{
		SiteURL:       "https://b.com",
		GroupsFailure: &model.OperationFailure{Category: model.ErrTimeout, Detail: "context deadline exceeded"},
	})

	outcomes := oc.SPOutcomes()
	if len(outcomes) != 2 {
		t.Fatalf("SPOutcomes len = %d, want 2", len(outcomes))
	}
	if outcomes[0].SiteURL != "https://a.com" {
		t.Errorf("SPOutcomes[0].SiteURL = %s, want https://a.com", outcomes[0].SiteURL)
	}
}

func TestOutcomeCollector_ODOutcomes(t *testing.T) {
	oc := &OutcomeCollector{}

	oc.RecordODOutcome(model.ODSiteOutcome{
		SiteURL:        "https://od.com",
		UserAccount:    "user@test.com",
		ProfileFailure: &model.OperationFailure{Category: model.ErrAuth, HTTPStatus: 403, Detail: "403 Forbidden"},
	})

	outcomes := oc.ODOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("ODOutcomes len = %d, want 1", len(outcomes))
	}
	if outcomes[0].UserAccount != "user@test.com" {
		t.Errorf("ODOutcomes[0].UserAccount = %s, want user@test.com", outcomes[0].UserAccount)
	}
}

func TestOutcomeCollector_FailureSummary(t *testing.T) {
	oc := &OutcomeCollector{}

	oc.RecordSPOutcome(model.SPSiteOutcome{
		SiteURL:      "https://a.com",
		UsersFailure: &model.OperationFailure{Category: model.ErrThrottle},
	})
	oc.RecordSPOutcome(model.SPSiteOutcome{
		SiteURL:      "https://b.com",
		UsersFailure: &model.OperationFailure{Category: model.ErrThrottle},
	})
	oc.RecordSPOutcome(model.SPSiteOutcome{
		SiteURL:       "https://c.com",
		GroupsFailure: &model.OperationFailure{Category: model.ErrTimeout},
	})
	oc.RecordODOutcome(model.ODSiteOutcome{
		SiteURL:        "https://od.com",
		ProfileFailure: &model.OperationFailure{Category: model.ErrAuth},
	})

	summary := oc.FailureSummary()
	if summary["users/throttle"] != 2 {
		t.Errorf("users/throttle = %d, want 2", summary["users/throttle"])
	}
	if summary["groups/timeout"] != 1 {
		t.Errorf("groups/timeout = %d, want 1", summary["groups/timeout"])
	}
	if summary["profile/auth"] != 1 {
		t.Errorf("profile/auth = %d, want 1", summary["profile/auth"])
	}
}
