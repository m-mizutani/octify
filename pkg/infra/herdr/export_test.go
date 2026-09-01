package herdr

// Sanitize exposes the field cleaner so its rules can be tested without a
// socket in front of them.
var Sanitize = sanitize

// Field limits the client applies, so a test does not restate the numbers.
const (
	MaxTitleRunes = maxTitleRunes
	MaxBodyRunes  = maxBodyRunes
)
