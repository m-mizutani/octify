package tokenstore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/octify/pkg/domain/model"
	"github.com/m-mizutani/octify/pkg/infra/tokenstore"
)

func sampleCredential() *model.Credential {
	return &model.Credential{
		Version:     model.CredentialVersion,
		Host:        "github.com",
		AccessToken: "gho_example_token",
		TokenType:   "bearer",
		Scope:       "repo,notifications",
		StoredAt:    time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC),
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credential.json")
	store := tokenstore.NewFile(path)

	gt.Equal(t, store.Backend(), tokenstore.BackendFile)

	backend := gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)
	gt.Equal(t, backend, tokenstore.BackendFile)

	loaded := gt.R1(store.Load(t.Context())).NoError(t)
	want := sampleCredential()
	gt.Equal(t, loaded.Version, want.Version)
	gt.Equal(t, loaded.Host, want.Host)
	gt.Equal(t, loaded.AccessToken, want.AccessToken)
	gt.Equal(t, loaded.TokenType, want.TokenType)
	gt.Equal(t, loaded.Scope, want.Scope)
	gt.True(t, loaded.StoredAt.Equal(want.StoredAt))

	fileInfo := gt.R1(os.Stat(path)).NoError(t)
	gt.Equal(t, fileInfo.Mode().Perm(), os.FileMode(0o600))

	dirInfo := gt.R1(os.Stat(filepath.Dir(path))).NoError(t)
	gt.Equal(t, dirInfo.Mode().Perm(), os.FileMode(0o700))

	gt.NoError(t, store.Delete(t.Context()))
	_, err := store.Load(t.Context())
	gt.Error(t, err).Is(tokenstore.ErrNotFound)
}

func TestFileStoreMissingFile(t *testing.T) {
	store := tokenstore.NewFile(filepath.Join(t.TempDir(), "absent.json"))

	_, err := store.Load(t.Context())
	gt.Error(t, err).Is(tokenstore.ErrNotFound)

	gt.Error(t, store.Delete(t.Context())).Is(tokenstore.ErrNotFound)
}

func TestFileStoreRejectsLoosePermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.json")
	store := tokenstore.NewFile(path)
	gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)

	gt.NoError(t, os.Chmod(path, 0o644))

	cred, err := store.Load(t.Context())
	gt.Error(t, err).Is(tokenstore.ErrInsecurePermission)
	gt.Nil(t, cred)

	// The advice must be something the user can run as-is.
	msg, ok := model.UserMessageOf(err)
	gt.True(t, ok)
	gt.S(t, msg.Action).Contains("chmod 600")
	gt.S(t, msg.Action).Contains(path)
}

func TestFileStoreBrokenContent(t *testing.T) {
	testCases := map[string]struct {
		content string
		wantErr error
	}{
		"not json": {
			content: `{"version": 1,`,
			wantErr: model.ErrInvalidCredential,
		},
		"unknown version": {
			content: `{"version": 99, "host": "github.com", "access_token": "t"}`,
			wantErr: model.ErrUnsupportedCredentialVersion,
		},
		"empty token": {
			content: `{"version": 1, "host": "github.com", "access_token": ""}`,
			wantErr: model.ErrInvalidCredential,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credential.json")
			gt.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))

			_, err := tokenstore.NewFile(path).Load(t.Context())
			gt.Error(t, err).Is(tc.wantErr)

			msg, ok := model.UserMessageOf(err)
			gt.True(t, ok)
			gt.NotEqual(t, msg.Summary, "")
		})
	}
}

func TestFileStoreLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential.json")
	store := tokenstore.NewFile(path)

	gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)
	gt.R1(store.Save(t.Context(), sampleCredential())).NoError(t)

	entries := gt.R1(os.ReadDir(dir)).NoError(t)
	gt.A(t, entries).Length(1)
	gt.Equal(t, entries[0].Name(), "credential.json")
}
