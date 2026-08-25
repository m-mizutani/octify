package tokenstore

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"runtime"

	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/utils/atomicfile"
)

const (
	credentialFileMode fs.FileMode = 0o600
	credentialDirMode  fs.FileMode = 0o700
)

type fileStore struct {
	path string
}

// NewFile stores the credential as JSON in a file only the owner can read.
func NewFile(path string) Store {
	return &fileStore{path: path}
}

func (s *fileStore) Backend() Backend { return BackendFile }

func (s *fileStore) Load(ctx context.Context) (*model.Credential, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, goerr.Wrap(ErrNotFound, "credential file does not exist", goerr.V("path", s.path))
		}
		return nil, goerr.Wrap(err, "failed to stat credential file", goerr.V("path", s.path))
	}

	// Refuse to read a token that other users on the machine can also read.
	//
	// Windows is excluded because it does not model Unix permission bits: Stat
	// reports 0666 (or 0444 when read-only) regardless of the ACL, and Chmod
	// only toggles the read-only attribute. Checking there would reject every
	// credential file octify itself wrote.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm&0o077 != 0 {
		return nil, model.WithUserMessage(
			goerr.Wrap(ErrInsecurePermission, "credential file is group or world accessible",
				goerr.V("path", s.path), goerr.V("mode", perm.String())),
			model.UserMessage{
				Summary: "the credential file is readable by others",
				Action:  "run: chmod 600 " + s.path,
			},
		)
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, goerr.Wrap(err, "failed to read credential file", goerr.V("path", s.path))
	}

	var cred model.Credential
	if err := json.Unmarshal(raw, &cred); err != nil {
		return nil, model.WithUserMessage(
			goerr.Wrap(model.ErrInvalidCredential, "credential file is not valid json",
				goerr.V("path", s.path), goerr.V("cause", err.Error())),
			model.UserMessage{
				Summary: "the saved credential is not usable",
				Action:  "press o to sign in again",
			},
		)
	}
	if err := cred.Validate(); err != nil {
		return nil, decorateCredentialError(err, model.UserMessage{
			Summary: "the credential file was written by a newer octify",
			Action:  "update octify, or delete " + s.path,
		})
	}
	return &cred, nil
}

func (s *fileStore) Save(ctx context.Context, cred *model.Credential) (Backend, error) {
	raw, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return "", goerr.Wrap(err, "failed to encode credential")
	}
	if err := atomicfile.Write(s.path, raw, credentialFileMode, credentialDirMode); err != nil {
		return "", err
	}
	return BackendFile, nil
}

func (s *fileStore) Delete(ctx context.Context) error {
	if err := os.Remove(s.path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return goerr.Wrap(ErrNotFound, "credential file does not exist", goerr.V("path", s.path))
		}
		return goerr.Wrap(err, "failed to delete credential file", goerr.V("path", s.path))
	}
	return nil
}
