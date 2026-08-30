package model

// SubjectState is what GitHub currently says about one issue or pull request.
type SubjectState struct {
	// Authored is true when the signed-in user opened it.
	Authored bool
	// Merged is true only for a merged pull request.
	Merged bool
	// Closed is true for a closed issue and for a pull request closed without
	// being merged. A merged pull request has Merged true and Closed false, so
	// that the two markers stay mutually exclusive.
	Closed bool
}

// Finished reports whether nothing further is expected on this subject.
func (s SubjectState) Finished() bool {
	return s.Merged || s.Closed
}

// SubjectStates is what one poll resolved. A subject GitHub declined to resolve
// is absent rather than present with a zero value, so that "unknown" and "open,
// not authored by you" stay distinguishable.
type SubjectStates map[SubjectRef]SubjectState

// Lookup returns the state for one subject, if it was resolved.
func (s SubjectStates) Lookup(ref SubjectRef) (SubjectState, bool) {
	if s == nil {
		return SubjectState{}, false
	}
	st, ok := s[ref]
	return st, ok
}
