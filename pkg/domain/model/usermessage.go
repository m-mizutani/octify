package model

import "github.com/m-mizutani/goerr/v2"

// UserMessage is the text shown to the person using octify. Summary states the
// cause in one sentence, Action states what they can do next and may be empty.
// Neither ends with a period: the caller joins and truncates them.
type UserMessage struct {
	Summary string
	Action  string
}

// userMessageKey carries the display text along the error chain.
//
// It is a goerr typed value rather than a wrapper type of our own so that the
// outermost error stays a *goerr.Error. The slog handler selects errors with a
// direct type assertion on that type; a custom wrapper would hide it and every
// goerr.V value attached further down the chain would be dropped from the log.
var userMessageKey = goerr.NewTypedKey[UserMessage]("user_message")

// WithUserMessage attaches display text to err. The wrapped error keeps working
// with errors.Is and errors.As, and keeps the values attached below it.
func WithUserMessage(err error, m UserMessage) error {
	if err == nil {
		return nil
	}
	return goerr.Wrap(err, m.Summary, goerr.TV(userMessageKey, m))
}

// UserMessageOf returns the outermost display text attached to err.
func UserMessageOf(err error) (UserMessage, bool) {
	return goerr.GetTypedValue(err, userMessageKey)
}
