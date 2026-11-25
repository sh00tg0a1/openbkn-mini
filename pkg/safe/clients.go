package safe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CLIRedirectURI is the loopback address the openbkn CLI listens on while the
// user signs in.
const CLIRedirectURI = "http://127.0.0.1:9010/callback"

type oauthClient struct {
	ClientID                string   `json:"client_id"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	RedirectURIs            []string `json:"redirect_uris,omitempty"`
	PostLogoutRedirectURIs  []string `json:"post_logout_redirect_uris,omitempty"`
	Scope                   string   `json:"scope"`
	Audience                []string `json:"audience"`
}

// clients are the ways a person signs in, as the platform registers them: the
// SDK and CLI by device code, studio and the CLI by authorization code with
// PKCE. The services need no client: they only check tokens.
func clients(publicURL string) []oauthClient {
	code := []string{"code"}
	audience := []string{"bkn-safe"}
	return []oauthClient{
		{
			ClientID:                "openbkn-sdk",
			GrantTypes:              []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
			ResponseTypes:           code,
			TokenEndpointAuthMethod: "none",
			Scope:                   "openid offline profile email phone",
			Audience:                audience,
		},
		{
			ClientID:                "openbkn-studio",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           code,
			TokenEndpointAuthMethod: "none",
			RedirectURIs:            []string{StudioRedirectURI(publicURL)},
			PostLogoutRedirectURIs:  []string{publicURL + "/studio"},
			Scope:                   "openid offline profile email phone",
			Audience:                audience,
		},
		{
			ClientID:                "openbkn-cli",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           code,
			TokenEndpointAuthMethod: "none",
			RedirectURIs:            []string{CLIRedirectURI},
			Scope:                   "openid offline all profile email phone",
			Audience:                audience,
		},
	}
}

// SeedClients registers the sign-in clients in Hydra if they are missing. An
// existing client is left as it is: bkn-safe keeps studio's callback addresses
// in step with the public URL and with the origins an administrator adds.
func SeedClients(cfg Config) error {
	admin := "http://" + cfg.HydraAdminAddr + "/admin/clients"
	httpClient := &http.Client{Timeout: 10 * time.Second}
	for _, c := range clients(cfg.PublicURL) {
		resp, err := httpClient.Get(admin + "/" + c.ClientID)
		if err != nil {
			return fmt.Errorf("look up OAuth client %s: %w", c.ClientID, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			continue
		}
		if resp.StatusCode != http.StatusNotFound {
			return fmt.Errorf("look up OAuth client %s: %s", c.ClientID, resp.Status)
		}
		body, err := json.Marshal(c)
		if err != nil {
			return err
		}
		resp, err = httpClient.Post(admin, "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("register OAuth client %s: %w", c.ClientID, err)
		}
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("register OAuth client %s: %s: %s", c.ClientID, resp.Status, answer)
		}
	}
	return nil
}
