// Package auth resolves an authenticated *http.Client for an org using its
// GitHub App installation. Installation tokens auto-refresh (~1h), are
// least-privilege, and raise rate limits. App auth is the only supported mode -
// see docs/GITHUB_APP_SETUP.md.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"os"

	githubauth "github.com/jferrl/go-githubauth"
	"golang.org/x/oauth2"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/secrets"
)

// HTTPClient builds an authenticated HTTP client for the given org's GitHub App
// installation.
func HTTPClient(ctx context.Context, org *config.OrgConfig, sec secrets.Store) (*http.Client, error) {
	if org.AppID == 0 || org.InstallationID == 0 {
		return nil, fmt.Errorf("org %s: appID and installationID are required (run `srm init`)", org.Name)
	}
	pem, err := appPrivateKey(org, sec)
	if err != nil {
		return nil, err
	}
	appTS, err := githubauth.NewApplicationTokenSource(org.AppID, pem)
	if err != nil {
		return nil, fmt.Errorf("org %s: build app token source: %w", org.Name, err)
	}
	// Installation token auto-refreshes (~1h) under the hood.
	instTS := githubauth.NewInstallationTokenSource(org.InstallationID, appTS)
	return oauth2.NewClient(ctx, instTS), nil
}

// AppHTTPClient builds an http client authenticated as the GitHub APP itself (a
// short-lived App JWT), NOT scoped to an installation. It is for app-level
// introspection the installation token cannot perform - notably reading the
// installation's granted permissions and repository selection (GET
// /orgs/{org}/installation), used to surface the silent "App sees only public
// repos" misconfiguration. InstallationID is not required here.
func AppHTTPClient(ctx context.Context, org *config.OrgConfig, sec secrets.Store) (*http.Client, error) {
	if org.AppID == 0 {
		return nil, fmt.Errorf("org %s: appID is required (run `srm init`)", org.Name)
	}
	pem, err := appPrivateKey(org, sec)
	if err != nil {
		return nil, err
	}
	appTS, err := githubauth.NewApplicationTokenSource(org.AppID, pem)
	if err != nil {
		return nil, fmt.Errorf("org %s: build app token source: %w", org.Name, err)
	}
	return oauth2.NewClient(ctx, appTS), nil
}

// appPrivateKey resolves the App private key: an explicit .pem path first, then
// the secrets store under "app_key:<org>".
func appPrivateKey(org *config.OrgConfig, sec secrets.Store) ([]byte, error) {
	if org.PrivateKeyPath != "" {
		b, err := os.ReadFile(org.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("org %s: read app key %s: %w", org.Name, org.PrivateKeyPath, err)
		}
		return b, nil
	}
	if v, err := sec.Get("app_key:" + org.Name); err == nil {
		return []byte(v), nil
	}
	return nil, fmt.Errorf("org %s: no App private key (set privateKeyPath or secret app_key:%s)", org.Name, org.Name)
}
