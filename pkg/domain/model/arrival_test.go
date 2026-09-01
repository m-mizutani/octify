package model_test

import (
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

func TestArrivals(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

	at := func(id types.ThreadID, updated time.Time) model.Notification {
		return model.Notification{ID: id, UpdatedAt: updated}
	}

	testCases := map[string]struct {
		prev []model.Notification
		next []model.Notification
		want []types.ThreadID
	}{
		"an id the previous list did not hold": {
			prev: []model.Notification{at("1", base)},
			next: []model.Notification{at("1", base), at("2", base)},
			want: []types.ThreadID{"2"},
		},
		"an id both lists hold, updated since": {
			prev: []model.Notification{at("1", base)},
			next: []model.Notification{at("1", base.Add(time.Minute))},
			want: []types.ThreadID{"1"},
		},
		"an id both lists hold, not updated": {
			prev: []model.Notification{at("1", base)},
			next: []model.Notification{at("1", base)},
			want: nil,
		},
		"an id both lists hold, with a timestamp that moved backwards": {
			prev: []model.Notification{at("1", base)},
			next: []model.Notification{at("1", base.Add(-time.Minute))},
			want: nil,
		},
		"an id only the previous list holds": {
			prev: []model.Notification{at("1", base), at("2", base)},
			next: []model.Notification{at("1", base)},
			want: nil,
		},
		"an empty previous list makes everything an arrival": {
			prev: nil,
			next: []model.Notification{at("1", base), at("2", base)},
			want: []types.ThreadID{"1", "2"},
		},
		"an empty next list": {
			prev: []model.Notification{at("1", base)},
			next: nil,
			want: nil,
		},
		"the order of the next list is preserved": {
			prev: []model.Notification{at("2", base)},
			next: []model.Notification{at("3", base), at("2", base.Add(time.Minute)), at("1", base)},
			want: []types.ThreadID{"3", "2", "1"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := model.Arrivals(tc.prev, tc.next)

			ids := make([]types.ThreadID, 0, len(got))
			for _, n := range got {
				ids = append(ids, n.ID)
			}
			gt.Equal(t, tc.want, orNil(ids))
		})
	}
}

// orNil normalises an empty slice to nil so that the expectation can be written
// as nil for the cases that produce nothing.
func orNil(ids []types.ThreadID) []types.ThreadID {
	if len(ids) == 0 {
		return nil
	}
	return ids
}
