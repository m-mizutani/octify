package model_test

import (
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

func TestCredentialValidate(t *testing.T) {
	valid := func() *model.Credential {
		return &model.Credential{
			Version:     model.CredentialVersion,
			Host:        "github.com",
			AccessToken: "gho_example",
			TokenType:   "bearer",
			Scope:       "repo,notifications",
			StoredAt:    time.Now(),
		}
	}

	t.Run("valid credential passes", func(t *testing.T) {
		gt.NoError(t, valid().Validate())
	})

	t.Run("empty token is rejected", func(t *testing.T) {
		c := valid()
		c.AccessToken = ""
		gt.Error(t, c.Validate()).Is(model.ErrInvalidCredential)
	})

	t.Run("nil credential is rejected", func(t *testing.T) {
		var c *model.Credential
		gt.Error(t, c.Validate()).Is(model.ErrInvalidCredential)
	})

	t.Run("unknown version is rejected", func(t *testing.T) {
		c := valid()
		c.Version = model.CredentialVersion + 1
		gt.Error(t, c.Validate()).Is(model.ErrUnsupportedCredentialVersion)
	})

	t.Run("zero version is rejected", func(t *testing.T) {
		c := valid()
		c.Version = 0
		gt.Error(t, c.Validate()).Is(model.ErrUnsupportedCredentialVersion)
	})
}

func TestAccessTokenIsHiddenFromFmt(t *testing.T) {
	token := types.AccessToken("gho_secret_value")

	gt.Equal(t, token.String(), "[REDACTED]")
	gt.NotEqual(t, fmtString("%s", token), "gho_secret_value")
	gt.NotEqual(t, fmtString("%v", token), "gho_secret_value")

	// The underlying value must still be reachable for the Authorization header.
	gt.Equal(t, string(token), "gho_secret_value")
}
