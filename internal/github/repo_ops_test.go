package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	googlegithub "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type repoAPIServer struct {
	t *testing.T

	orgRepos  []*googlegithub.Repository
	userRepos []*googlegithub.Repository
	authRepos []*googlegithub.Repository
}

func (s *repoAPIServer) handler(w http.ResponseWriter, r *http.Request) {
	s.t.Helper()
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/orgs/acme/repos":
		require.NoError(s.t, json.NewEncoder(w).Encode(s.orgRepos))
	case r.Method == http.MethodGet && r.URL.Path == "/orgs/jane/repos":
		w.WriteHeader(http.StatusNotFound)
		require.NoError(s.t, json.NewEncoder(w).Encode(map[string]any{"message": "not found"}))
	case r.Method == http.MethodGet && r.URL.Path == "/users/jane/repos":
		require.NoError(s.t, json.NewEncoder(w).Encode(s.userRepos))
	case r.Method == http.MethodGet && r.URL.Path == "/user/repos":
		require.NoError(s.t, json.NewEncoder(w).Encode(s.authRepos))
	default:
		http.NotFound(w, r)
	}
}

func TestListOwnerRepos_FiltersArchivedAndFallsBackToAuthenticatedUser(t *testing.T) {
	api := &repoAPIServer{
		t: t,
		orgRepos: []*googlegithub.Repository{
			{FullName: ptr("acme/api"), Archived: ptr(false)},
			{FullName: ptr("acme/old"), Archived: ptr(true)},
		},
		userRepos: []*googlegithub.Repository{
			{
				FullName: ptr("jane/app"),
				Archived: ptr(false),
				Owner:    &googlegithub.User{Login: ptr("jane")},
			},
		},
		authRepos: []*googlegithub.Repository{
			{
				FullName: ptr("jane/private"),
				Archived: ptr(false),
				Owner:    &googlegithub.User{Login: ptr("jane")},
			},
			{
				FullName: ptr("other/nope"),
				Archived: ptr(false),
				Owner:    &googlegithub.User{Login: ptr("other")},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(api.handler))
	defer srv.Close()

	client, err := NewClient("", WithBaseURL(srv.URL+"/"))
	require.NoError(t, err)

	orgRepos, err := client.ListOwnerRepos(context.Background(), "acme", 1000)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, orgRepos)

	userRepos, err := client.ListOwnerRepos(context.Background(), "jane", 1000)
	require.NoError(t, err)
	assert.Equal(t, []string{"jane/app", "jane/private"}, userRepos)
}
