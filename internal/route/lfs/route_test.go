// Copyright 2020 The Gogs Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package lfs

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/macaron.v1"

	"gogs.io/gogs/internal/auth"
	"gogs.io/gogs/internal/db"
	"gogs.io/gogs/internal/lfsutil"
)

func Test_authenticate(t *testing.T) {
	m := macaron.New()
	m.Use(macaron.Renderer())
	m.Get("/", authenticate(), func(w http.ResponseWriter, user *db.User) {
		fmt.Fprintf(w, "ID: %d, Name: %s", user.ID, user.Name)
	})

	tests := []struct {
		name                  string
		header                http.Header
		mockUsersStore        *db.MockUsersStore
		mockTwoFactorsStore   *db.MockTwoFactorsStore
		mockAccessTokensStore func() db.AccessTokensStore
		expStatusCode         int
		expHeader             http.Header
		expBody               string
	}{
		{
			name:          "no authorization",
			expStatusCode: http.StatusUnauthorized,
			expHeader: http.Header{
				"Lfs-Authenticate": []string{`Basic realm="Git LFS"`},
				"Content-Type":     []string{"application/vnd.git-lfs+json"},
			},
			expBody: `{"message":"Credentials needed"}` + "\n",
		},
		{
			name: "user has 2FA enabled",
			header: http.Header{
				"Authorization": []string{"Basic dXNlcm5hbWU6cGFzc3dvcmQ="},
			},
			mockUsersStore: &db.MockUsersStore{
				MockAuthenticate: func(username, password string, loginSourceID int64) (*db.User, error) {
					return &db.User{}, nil
				},
			},
			mockTwoFactorsStore: &db.MockTwoFactorsStore{
				MockIsUserEnabled: func(userID int64) bool {
					return true
				},
			},
			expStatusCode: http.StatusBadRequest,
			expHeader:     http.Header{},
			expBody:       "Users with 2FA enabled are not allowed to authenticate via username and password.",
		},
		{
			name: "both user and access token do not exist",
			header: http.Header{
				"Authorization": []string{"Basic dXNlcm5hbWU="},
			},
			mockUsersStore: &db.MockUsersStore{
				MockAuthenticate: func(username, password string, loginSourceID int64) (*db.User, error) {
					return nil, auth.ErrBadCredentials{}
				},
			},
			mockAccessTokensStore: func() db.AccessTokensStore {
				mock := db.NewMockAccessTokensStore()
				mock.GetBySHA1Func.SetDefaultReturn(nil, db.ErrAccessTokenNotExist{})
				return mock
			},
			expStatusCode: http.StatusUnauthorized,
			expHeader: http.Header{
				"Lfs-Authenticate": []string{`Basic realm="Git LFS"`},
				"Content-Type":     []string{"application/vnd.git-lfs+json"},
			},
			expBody: `{"message":"Credentials needed"}` + "\n",
		},

		{
			name: "authenticated by username and password",
			header: http.Header{
				"Authorization": []string{"Basic dXNlcm5hbWU6cGFzc3dvcmQ="},
			},
			mockUsersStore: &db.MockUsersStore{
				MockAuthenticate: func(username, password string, loginSourceID int64) (*db.User, error) {
					return &db.User{ID: 1, Name: "unknwon"}, nil
				},
			},
			mockTwoFactorsStore: &db.MockTwoFactorsStore{
				MockIsUserEnabled: func(userID int64) bool {
					return false
				},
			},
			expStatusCode: http.StatusOK,
			expHeader:     http.Header{},
			expBody:       "ID: 1, Name: unknwon",
		},
		{
			name: "authenticate by access token",
			header: http.Header{
				"Authorization": []string{"Basic dXNlcm5hbWU="},
			},
			mockUsersStore: &db.MockUsersStore{
				MockAuthenticate: func(username, password string, loginSourceID int64) (*db.User, error) {
					return nil, auth.ErrBadCredentials{}
				},
				MockGetByID: func(id int64) (*db.User, error) {
					return &db.User{ID: 1, Name: "unknwon"}, nil
				},
			},
			mockAccessTokensStore: func() db.AccessTokensStore {
				mock := db.NewMockAccessTokensStore()
				mock.GetBySHA1Func.SetDefaultReturn(&db.AccessToken{}, nil)
				return mock
			},
			expStatusCode: http.StatusOK,
			expHeader:     http.Header{},
			expBody:       "ID: 1, Name: unknwon",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db.SetMockUsersStore(t, test.mockUsersStore)
			db.SetMockTwoFactorsStore(t, test.mockTwoFactorsStore)

			if test.mockAccessTokensStore != nil {
				db.SetMockAccessTokensStore(t, test.mockAccessTokensStore())
			}

			r, err := http.NewRequest("GET", "/", nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header = test.header

			rr := httptest.NewRecorder()
			m.ServeHTTP(rr, r)

			resp := rr.Result()
			assert.Equal(t, test.expStatusCode, resp.StatusCode)
			assert.Equal(t, test.expHeader, resp.Header)

			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			assert.Equal(t, test.expBody, string(body))
		})
	}
}

func Test_authorize(t *testing.T) {
	tests := []struct {
		name           string
		authroize      macaron.Handler
		mockUsersStore *db.MockUsersStore
		mockReposStore *db.MockReposStore
		mockPermsStore func() db.PermsStore
		expStatusCode  int
		expBody        string
	}{
		{
			name:      "user does not exist",
			authroize: authorize(db.AccessModeNone),
			mockUsersStore: &db.MockUsersStore{
				MockGetByUsername: func(username string) (*db.User, error) {
					return nil, db.ErrUserNotExist{}
				},
			},
			expStatusCode: http.StatusNotFound,
		},
		{
			name:      "repository does not exist",
			authroize: authorize(db.AccessModeNone),
			mockUsersStore: &db.MockUsersStore{
				MockGetByUsername: func(username string) (*db.User, error) {
					return &db.User{Name: username}, nil
				},
			},
			mockReposStore: &db.MockReposStore{
				MockGetByName: func(ownerID int64, name string) (*db.Repository, error) {
					return nil, db.ErrRepoNotExist{}
				},
			},
			expStatusCode: http.StatusNotFound,
		},
		{
			name:      "actor is not authorized",
			authroize: authorize(db.AccessModeWrite),
			mockUsersStore: &db.MockUsersStore{
				MockGetByUsername: func(username string) (*db.User, error) {
					return &db.User{Name: username}, nil
				},
			},
			mockReposStore: &db.MockReposStore{
				MockGetByName: func(ownerID int64, name string) (*db.Repository, error) {
					return &db.Repository{Name: name}, nil
				},
			},
			mockPermsStore: func() db.PermsStore {
				mock := db.NewMockPermsStore()
				mock.AuthorizeFunc.SetDefaultHook(func(ctx context.Context, userID int64, repoID int64, desired db.AccessMode, opts db.AccessModeOptions) bool {
					return desired <= db.AccessModeRead
				})
				return mock
			},
			expStatusCode: http.StatusNotFound,
		},

		{
			name:      "actor is authorized",
			authroize: authorize(db.AccessModeRead),
			mockUsersStore: &db.MockUsersStore{
				MockGetByUsername: func(username string) (*db.User, error) {
					return &db.User{Name: username}, nil
				},
			},
			mockReposStore: &db.MockReposStore{
				MockGetByName: func(ownerID int64, name string) (*db.Repository, error) {
					return &db.Repository{Name: name}, nil
				},
			},
			mockPermsStore: func() db.PermsStore {
				mock := db.NewMockPermsStore()
				mock.AuthorizeFunc.SetDefaultHook(func(ctx context.Context, userID int64, repoID int64, desired db.AccessMode, opts db.AccessModeOptions) bool {
					return desired <= db.AccessModeRead
				})
				return mock
			},
			expStatusCode: http.StatusOK,
			expBody:       "owner.Name: owner, repo.Name: repo",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db.SetMockUsersStore(t, test.mockUsersStore)
			db.SetMockReposStore(t, test.mockReposStore)

			if test.mockPermsStore != nil {
				db.SetMockPermsStore(t, test.mockPermsStore())
			}

			m := macaron.New()
			m.Use(macaron.Renderer())
			m.Use(func(c *macaron.Context) {
				c.Map(&db.User{})
			})
			m.Get("/:username/:reponame", test.authroize, func(w http.ResponseWriter, owner *db.User, repo *db.Repository) {
				fmt.Fprintf(w, "owner.Name: %s, repo.Name: %s", owner.Name, repo.Name)
			})

			r, err := http.NewRequest("GET", "/owner/repo", nil)
			if err != nil {
				t.Fatal(err)
			}

			rr := httptest.NewRecorder()
			m.ServeHTTP(rr, r)

			resp := rr.Result()
			assert.Equal(t, test.expStatusCode, resp.StatusCode)

			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			assert.Equal(t, test.expBody, string(body))
		})
	}
}

func Test_verifyHeader(t *testing.T) {
	tests := []struct {
		name          string
		verifyHeader  macaron.Handler
		header        http.Header
		expStatusCode int
	}{
		{
			name:          "header not found",
			verifyHeader:  verifyHeader("Accept", contentType, http.StatusNotAcceptable),
			expStatusCode: http.StatusNotAcceptable,
		},

		{
			name:         "header found",
			verifyHeader: verifyHeader("Accept", "application/vnd.git-lfs+json", http.StatusNotAcceptable),
			header: http.Header{
				"Accept": []string{"application/vnd.git-lfs+json; charset=utf-8"},
			},
			expStatusCode: http.StatusOK,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := macaron.New()
			m.Use(macaron.Renderer())
			m.Get("/", test.verifyHeader)

			r, err := http.NewRequest("GET", "/", nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header = test.header

			rr := httptest.NewRecorder()
			m.ServeHTTP(rr, r)

			resp := rr.Result()
			assert.Equal(t, test.expStatusCode, resp.StatusCode)
		})
	}
}

func Test_verifyOID(t *testing.T) {
	m := macaron.New()
	m.Get("/:oid", verifyOID(), func(w http.ResponseWriter, oid lfsutil.OID) {
		fmt.Fprintf(w, "oid: %s", oid)
	})

	tests := []struct {
		name          string
		url           string
		expStatusCode int
		expBody       string
	}{
		{
			name:          "bad oid",
			url:           "/bad_oid",
			expStatusCode: http.StatusBadRequest,
			expBody:       `{"message":"Invalid oid"}` + "\n",
		},

		{
			name:          "good oid",
			url:           "/ef797c8118f02dfb649607dd5d3f8c7623048c9c063d532cc95c5ed7a898a64f",
			expStatusCode: http.StatusOK,
			expBody:       "oid: ef797c8118f02dfb649607dd5d3f8c7623048c9c063d532cc95c5ed7a898a64f",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, err := http.NewRequest("GET", test.url, nil)
			if err != nil {
				t.Fatal(err)
			}

			rr := httptest.NewRecorder()
			m.ServeHTTP(rr, r)

			resp := rr.Result()
			assert.Equal(t, test.expStatusCode, resp.StatusCode)

			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			assert.Equal(t, test.expBody, string(body))
		})
	}
}

func Test_internalServerError(t *testing.T) {
	rr := httptest.NewRecorder()
	internalServerError(rr)

	resp := rr.Result()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, `{"message":"Internal server error"}`+"\n", string(body))
}
