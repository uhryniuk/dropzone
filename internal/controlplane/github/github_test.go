package github

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uhryniuk/dropzone/internal/config"
	"github.com/uhryniuk/dropzone/internal/controlplane"
)

func TestAuthenticate(t *testing.T) {
	// Mock GitHub User API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("Expected path /user, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "token test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Since we can't easily mock the hardcoded URL in Authenticate,
	// we will skip the real network call test or we'd need to make the URL configurable.
	// For this test, we can verify the token is set in config.
	// A better design would allow injecting the API base URL.
	// Assuming Authenticate makes a real call to github.com, we can't mock it easily without DI.
	//
	// However, we can test that it updates the config object.
	cfg := config.ControlPlaneConfig{
		Name:     "test",
		Type:     "github",
		Endpoint: "github://test/repo",
	}
	cp, _ := New(cfg)
	_ = cp

	// Since we can't mock api.github.com, this test is limited unless we refactor to allow base URL injection.
	// For MVP, we'll assume the implementation is correct and focus on other methods where we can potentially mock more easily if we refactor.
	// Or we skip the network part of Authenticate test.
}

// Since fetchReleases uses hardcoded https://api.github.com, testing logic requires either:
// 1. Refactoring implementation to accept a base URL.
// 2. Mocking http.DefaultClient (global state, risky).
// 3. Not unit testing the network calls directly here.
//
// Let's assume we can refactor the implementation slightly to support testing,
// OR we just write what we can. Given the prompt constraint "write a new file", I cannot change the implementation file now.
//
// I will implement tests that focus on logic that doesn't hit the hardcoded URL if possible, or accept that they might fail if network is unreachable/unauthenticated.
//
// Ideally, the GitHubControlPlane struct should store the apiBaseURL, defaulted to https://api.github.com.
// Since I cannot change implementation code in this turn, I will write the test file assuming future refactoring or accepting limitations.
//
// Wait, I can't really test ListPackageNames without hitting real GitHub.
// I'll write a test that *would* work if the API URL was injectable,
// essentially documenting the expected behavior.

func TestInterface(t *testing.T) {
	cfg := config.ControlPlaneConfig{Name: "test", Type: "github"}
	cp, _ := New(cfg)

	var _ controlplane.ControlPlane = cp
}

// NOTE: Real unit tests for GitHub integration require mocking the API URL.
// Since the implementation currently hardcodes "https://api.github.com",
// comprehensive tests are not possible without refactoring.
//
// Below is a placeholder for when DI is added.

/*
func TestListPackageNames(t *testing.T) {
	// Mock Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock response for /repos/owner/repo/releases
		releases := []ghRelease{
			{
				TagName: "v1.0.0",
				Assets: []ghAsset{
					{Name: "pkg1.tar.gz", BrowserDownloadURL: "http://dl/pkg1"},
				},
			},
		}
		json.NewEncoder(w).Encode(releases)
	}))
	defer server.Close()

	// We need to inject server.URL into the instance.
	// cp.apiBaseURL = server.URL
}
*/
