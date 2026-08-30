package model_test

import (
	"testing"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
)

func TestSubjectStateFinished(t *testing.T) {
	testCases := map[string]struct {
		state model.SubjectState
		want  bool
	}{
		"merged":            {state: model.SubjectState{Merged: true}, want: true},
		"closed":            {state: model.SubjectState{Closed: true}, want: true},
		"open":              {state: model.SubjectState{}, want: false},
		"open and authored": {state: model.SubjectState{Authored: true}, want: false},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gt.Equal(t, tc.state.Finished(), tc.want)
		})
	}
}

func TestSubjectStatesLookup(t *testing.T) {
	ref := model.SubjectRef{Repo: "acme/tools", Number: 12}
	states := model.SubjectStates{ref: {Authored: true, Merged: true}}

	got, ok := states.Lookup(ref)
	gt.True(t, ok)
	gt.Equal(t, got, model.SubjectState{Authored: true, Merged: true})

	// An unresolved subject is absent, not present with a zero value: the caller
	// has to tell "unknown" from "open and not yours".
	_, ok = states.Lookup(model.SubjectRef{Repo: "acme/tools", Number: 13})
	gt.False(t, ok)

	var empty model.SubjectStates
	_, ok = empty.Lookup(ref)
	gt.False(t, ok)
}
