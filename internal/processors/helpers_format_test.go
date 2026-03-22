package processors

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sprawler/internal/model"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"sub-minute", 30*time.Second + 500*time.Millisecond, "30.5s"},
		{"minutes", 5*time.Minute + 30*time.Second, "5.5m"},
		{"hours", 2*time.Hour + 12*time.Minute, "2.2h"},
		{"zero", 0, "0.0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatDuration(tt.d))
		})
	}
}

func TestFormatFailureBreakdown(t *testing.T) {
	t.Run("empty collector", func(t *testing.T) {
		oc := &OutcomeCollector{}
		assert.Empty(t, formatFailureBreakdown(oc))
	})

	t.Run("multiple categories sorted", func(t *testing.T) {
		oc := &OutcomeCollector{}
		oc.RecordSPOutcome(model.SPSiteOutcome{
			UsersFailure: &model.OperationFailure{Category: model.ErrTimeout},
		})
		oc.RecordSPOutcome(model.SPSiteOutcome{
			GroupsFailure: &model.OperationFailure{Category: model.ErrAuth},
		})
		oc.RecordSPOutcome(model.SPSiteOutcome{
			UsersFailure: &model.OperationFailure{Category: model.ErrTimeout},
		})

		got := formatFailureBreakdown(oc)
		assert.Equal(t, []string{"1 groups/auth", "2 users/timeout"}, got)
	})
}
