package logging_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
	"github.com/m-mizutani/octify/pkg/utils/logging"
)

const secret = "gho_super_secret_token_value"

func TestFormatValidate(t *testing.T) {
	gt.NoError(t, logging.FormatText.Validate())
	gt.NoError(t, logging.FormatJSON.Validate())
	gt.Error(t, logging.Format("yaml").Validate()).Is(logging.ErrInvalidLogFormat)
	gt.Error(t, logging.Format("").Validate()).Is(logging.ErrInvalidLogFormat)
}

func TestTokenIsRedactedByType(t *testing.T) {
	for _, format := range []logging.Format{logging.FormatText, logging.FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			var buf bytes.Buffer
			logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelDebug, Format: format})

			logger.Info("using token", slog.Any("token", types.AccessToken(secret)))

			gt.S(t, buf.String()).ContainsNone(secret)
			gt.S(t, buf.String()).Contains("using token")
		})
	}
}

func TestTokenIsRedactedInsideCredential(t *testing.T) {
	for _, format := range []logging.Format{logging.FormatText, logging.FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			var buf bytes.Buffer
			logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelDebug, Format: format})

			logger.Info("credential restored", slog.Any("credential", &model.Credential{
				Version:     model.CredentialVersion,
				Host:        "github.com",
				AccessToken: types.AccessToken(secret),
				TokenType:   "bearer",
				Scope:       "repo,notifications",
				StoredAt:    time.Now(),
			}))

			gt.S(t, buf.String()).ContainsNone(secret)
			// Non-secret fields must stay visible for diagnosis.
			gt.S(t, buf.String()).Contains("github.com")
		})
	}
}

func TestDeviceCodeIsRedactedButUserCodeIsNot(t *testing.T) {
	type deviceCode struct {
		DeviceCode string
		UserCode   string
	}

	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelDebug, Format: logging.FormatJSON})
	logger.Info("device flow started", slog.Any("device", deviceCode{
		DeviceCode: "dc-secret-value",
		UserCode:   "ABCD-EFGH",
	}))

	gt.S(t, buf.String()).ContainsNone("dc-secret-value")
	// The user code is meant to be read aloud, so it must not be hidden.
	gt.S(t, buf.String()).Contains("ABCD-EFGH")
}

func TestTokenIsHiddenFromFormatting(t *testing.T) {
	token := types.AccessToken(secret)
	gt.NotEqual(t, fmt.Sprintf("%s", token), secret)
	gt.NotEqual(t, fmt.Sprintf("%v", token), secret)
	gt.Equal(t, fmt.Sprintf("%s", token), "[REDACTED]")
}

func TestJSONFormatIsMachineReadable(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelInfo, Format: logging.FormatJSON})
	logger.Info("hello", slog.String("key", "value"))

	var record map[string]any
	gt.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	gt.Equal(t, record["msg"], "hello")
	gt.Equal(t, record["key"], "value")
}

func TestTextFormatIsNotJSONAndHasNoColor(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelInfo, Format: logging.FormatText})
	logger.Info("hello", slog.String("key", "value"))

	out := buf.String()
	gt.S(t, out).Contains("hello")

	var record map[string]any
	gt.Error(t, json.Unmarshal(buf.Bytes(), &record))

	// The destination is a file, so escape sequences would only be noise.
	gt.S(t, out).ContainsNone("\x1b[")
}

func TestNilWriterDiscards(t *testing.T) {
	logger := logging.New(logging.Config{Level: slog.LevelDebug, Format: logging.FormatText})
	logger.Error("this must not panic", slog.String("key", "value"))
}

func TestErrorValuesReachTheLog(t *testing.T) {
	for _, format := range []logging.Format{logging.FormatText, logging.FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			var buf bytes.Buffer
			logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelDebug, Format: format})

			err := goerr.Wrap(goerr.New("inner failure"), "outer context",
				goerr.V("thread_id", "13845982"), goerr.V("status", 503))
			logger.Error("poll failed", slog.Any("error", err))

			out := buf.String()
			gt.S(t, out).Contains("13845982")
			gt.S(t, out).Contains("503")
			gt.S(t, out).Contains("outer context")
		})
	}
}

// Display text is attached to nearly every error that leaves the infrastructure
// layer. If that hid the diagnostic values, the log would be useless exactly
// where it matters most.
func TestErrorValuesSurviveUserMessage(t *testing.T) {
	for _, format := range []logging.Format{logging.FormatText, logging.FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			var buf bytes.Buffer
			logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelDebug, Format: format})

			err := model.WithUserMessage(
				goerr.Wrap(goerr.New("inner failure"), "outer context",
					goerr.V("thread_id", "13845982"), goerr.V("status", 503)),
				model.UserMessage{Summary: "GitHub returned 503", Action: "retrying in 2m0s"},
			)
			logger.Error("poll failed", slog.Any("error", err))

			out := buf.String()
			gt.S(t, out).Contains("13845982")
			gt.S(t, out).Contains("503")
			gt.S(t, out).Contains("outer context")
		})
	}
}

func TestContextLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf, Level: slog.LevelInfo, Format: logging.FormatJSON})

	ctx := logging.With(t.Context(), logger)
	logging.From(ctx).Info("from context")
	gt.S(t, buf.String()).Contains("from context")

	// Without a logger in the context, output is discarded rather than lost to a
	// nil dereference.
	logging.From(t.Context()).Info("nowhere")
}
