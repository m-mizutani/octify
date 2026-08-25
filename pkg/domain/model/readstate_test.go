package model_test

import (
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
)

func TestReadStateValidate(t *testing.T) {
	gt.NoError(t, model.ReadStateRead.Validate())
	gt.NoError(t, model.ReadStateUnread.Validate())
	gt.Error(t, model.ReadState("archived").Validate()).Is(model.ErrInvalidReadState)
	gt.Error(t, model.ReadState("").Validate()).Is(model.ErrInvalidReadState)
}

func TestNotificationUnread(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

	testCases := map[string]struct {
		serverUnread bool
		updatedAt    time.Time
		override     model.ReadOverride
		hasOverride  bool
		want         bool
	}{
		"no record and server says unread": {
			serverUnread: true,
			updatedAt:    base,
			want:         true,
		},
		"no record and server says read": {
			serverUnread: false,
			updatedAt:    base,
			want:         false,
		},
		"record says read and nothing changed": {
			serverUnread: true,
			updatedAt:    base,
			override:     model.ReadOverride{State: model.ReadStateRead, At: base, SubjectUpdatedAt: base},
			hasOverride:  true,
			want:         false,
		},
		"record says unread and nothing changed, overriding a read server flag": {
			serverUnread: false,
			updatedAt:    base,
			override:     model.ReadOverride{State: model.ReadStateUnread, At: base, SubjectUpdatedAt: base},
			hasOverride:  true,
			want:         true,
		},
		"record says read but the notification was updated again": {
			serverUnread: true,
			updatedAt:    base.Add(time.Minute),
			override:     model.ReadOverride{State: model.ReadStateRead, At: base, SubjectUpdatedAt: base},
			hasOverride:  true,
			want:         true,
		},
		"record says unread but the notification was updated again": {
			serverUnread: false,
			updatedAt:    base.Add(time.Minute),
			override:     model.ReadOverride{State: model.ReadStateUnread, At: base, SubjectUpdatedAt: base},
			hasOverride:  true,
			want:         false,
		},
		"a timestamp that moved backwards keeps the record": {
			serverUnread: true,
			updatedAt:    base.Add(-time.Minute),
			override:     model.ReadOverride{State: model.ReadStateRead, At: base, SubjectUpdatedAt: base},
			hasOverride:  true,
			want:         false,
		},
		"both timestamps zero keeps the record": {
			serverUnread: true,
			override:     model.ReadOverride{State: model.ReadStateRead},
			hasOverride:  true,
			want:         false,
		},
		"a record written against a zero timestamp is stale once a real one arrives": {
			serverUnread: true,
			updatedAt:    base,
			override:     model.ReadOverride{State: model.ReadStateRead, At: base},
			hasOverride:  true,
			want:         true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			n := model.Notification{ServerUnread: tc.serverUnread, UpdatedAt: tc.updatedAt}
			gt.Equal(t, n.Unread(tc.override, tc.hasOverride), tc.want)
		})
	}
}

// A notification whose updated_at GitHub sent in an unparsable form arrives
// with a zero UpdatedAt. There is then no ordering information at all, and the
// choice is between honouring the record (marking read works, but the record
// never expires on its own) and ignoring it (marking read never works). The
// first is the useful one, and the retention rule still clears the record once
// the notification leaves the list.
func TestUnreadWithUnorderableTimestamp(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	ov := model.ReadOverride{State: model.ReadStateRead, At: base, SubjectUpdatedAt: base}

	n := model.Notification{ServerUnread: true}
	gt.False(t, n.OverrideStale(ov))
	gt.False(t, n.Unread(ov, true))
}

func TestNotificationOverrideStale(t *testing.T) {
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	ov := model.ReadOverride{State: model.ReadStateRead, At: base, SubjectUpdatedAt: base}

	gt.False(t, model.Notification{UpdatedAt: base}.OverrideStale(ov))
	gt.False(t, model.Notification{UpdatedAt: base.Add(-time.Second)}.OverrideStale(ov))
	gt.True(t, model.Notification{UpdatedAt: base.Add(time.Nanosecond)}.OverrideStale(ov))
	gt.True(t, model.Notification{UpdatedAt: base.Add(time.Hour)}.OverrideStale(ov))
}
