package model_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
)

func fmtString(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

var errSentinel = goerr.New("sentinel for tests")

func TestUserMessageRoundTrip(t *testing.T) {
	msg := model.UserMessage{Summary: "something is wrong", Action: "press o"}

	t.Run("attached message is returned", func(t *testing.T) {
		got, ok := model.UserMessageOf(model.WithUserMessage(errSentinel, msg))
		gt.True(t, ok)
		gt.Equal(t, got, msg)
	})

	t.Run("message survives wrapping", func(t *testing.T) {
		err := model.WithUserMessage(errSentinel, msg)
		for i := range 3 {
			err = goerr.Wrap(err, fmt.Sprintf("layer %d", i))
		}
		got, ok := model.UserMessageOf(err)
		gt.True(t, ok)
		gt.Equal(t, got, msg)
	})

	t.Run("plain error has no message", func(t *testing.T) {
		_, ok := model.UserMessageOf(errSentinel)
		gt.False(t, ok)
	})

	t.Run("nil error has no message", func(t *testing.T) {
		_, ok := model.UserMessageOf(nil)
		gt.False(t, ok)
	})

	t.Run("attaching to nil returns nil", func(t *testing.T) {
		gt.Nil(t, model.WithUserMessage(nil, msg))
	})
}

func TestUserMessageOuterWins(t *testing.T) {
	inner := model.UserMessage{Summary: "inner", Action: "inner action"}
	outer := model.UserMessage{Summary: "outer", Action: "outer action"}

	err := model.WithUserMessage(errSentinel, inner)
	err = goerr.Wrap(err, "in between")
	err = model.WithUserMessage(err, outer)

	got, ok := model.UserMessageOf(err)
	gt.True(t, ok)
	gt.Equal(t, got, outer)
}

func TestUserMessageKeepsErrorDiscrimination(t *testing.T) {
	msg := model.UserMessage{Summary: "wrapped"}

	t.Run("errors.Is still matches the sentinel", func(t *testing.T) {
		err := model.WithUserMessage(goerr.Wrap(errSentinel, "context"), msg)
		gt.True(t, errors.Is(err, errSentinel))
	})

	t.Run("errors.As still finds a typed error", func(t *testing.T) {
		err := model.WithUserMessage(goerr.Wrap(&typedError{code: 42}, "context"), msg)
		var target *typedError
		gt.True(t, errors.As(err, &target))
		gt.Equal(t, target.code, 42)
	})

	t.Run("Error still reaches the underlying cause", func(t *testing.T) {
		err := model.WithUserMessage(errSentinel, msg)
		gt.S(t, err.Error()).Contains(errSentinel.Error())
	})

	t.Run("values attached below survive", func(t *testing.T) {
		err := model.WithUserMessage(
			goerr.Wrap(errSentinel, "context", goerr.V("thread_id", "13845982")),
			msg,
		)
		gt.Equal(t, goerr.Values(err)["thread_id"], "13845982")
	})
}

type typedError struct {
	code int
}

func (e *typedError) Error() string { return fmt.Sprintf("typed error %d", e.code) }
