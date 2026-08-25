package model

import (
	"time"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/types"
)

// CredentialVersion is the format version written by this build. A file with a
// different version is rejected rather than silently reinterpreted.
const CredentialVersion = 1

// Credential is the saved GitHub access token and what it was granted.
type Credential struct {
	Version     int               `json:"version"`
	Host        string            `json:"host"`
	AccessToken types.AccessToken `json:"access_token"`
	TokenType   string            `json:"token_type"`
	Scope       string            `json:"scope"`
	StoredAt    time.Time         `json:"stored_at"`
}

var (
	ErrInvalidCredential            = goerr.New("invalid credential")
	ErrUnsupportedCredentialVersion = goerr.New("unsupported credential version")
)

func (c *Credential) Validate() error {
	if c == nil {
		return goerr.Wrap(ErrInvalidCredential, "credential is nil")
	}
	if c.Version != CredentialVersion {
		return goerr.Wrap(ErrUnsupportedCredentialVersion, "credential version mismatch",
			goerr.V("version", c.Version), goerr.V("supported", CredentialVersion))
	}
	if c.AccessToken == "" {
		return goerr.Wrap(ErrInvalidCredential, "access token is empty")
	}
	return nil
}
