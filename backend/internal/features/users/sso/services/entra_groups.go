package sso_services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// entraGraphMemberObjectsURL lists the object IDs of every group the signed-in
// user belongs to. A variable only so tests can point it at a fake server.
var entraGraphMemberObjectsURL = "https://graph.microsoft.com/v1.0/me/getMemberObjects"

const (
	entraGraphMaxPages    = 20
	entraGraphMaxBodySize = 5 << 20
)

// hasGroupsOverage reports whether an Entra ID token uses the "group overage"
// escape hatch: a user in too many groups (about 200) gets no groups claim,
// only a _claim_names entry saying the groups must be fetched from Graph.
func hasGroupsOverage(rawClaims map[string]any, groupsClaim string) bool {
	claimNames, ok := rawClaims["_claim_names"].(map[string]any)
	if !ok {
		return false
	}
	_, overage := claimNames[groupsClaim]
	return overage
}

// fetchEntraGroupIDs resolves a user's group object IDs from Microsoft Graph
// with the access token from the sign-in (Entra issues a Graph token for the
// openid/profile scopes). The Graph URL is fixed rather than taken from the
// token's _claim_sources, so a token can't point this server at another host.
func fetchEntraGroupIDs(ctx context.Context, accessToken string) ([]string, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("no access token to call Microsoft Graph")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	url := entraGraphMemberObjectsURL
	body := []byte(`{"securityEnabledOnly":false}`)

	var ids []string
	for range entraGraphMaxPages {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+accessToken)
		request.Header.Set("Content-Type", "application/json")

		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("microsoft graph request failed: %w", err)
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, entraGraphMaxBodySize))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("failed to read microsoft graph response: %w", readErr)
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("microsoft graph returned status %d", response.StatusCode)
		}

		var page struct {
			Value    []string `json:"value"`
			NextLink string   `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(payload, &page); err != nil {
			return nil, fmt.Errorf("failed to decode microsoft graph response: %w", err)
		}
		ids = append(ids, page.Value...)

		if page.NextLink == "" {
			return ids, nil
		}
		url = page.NextLink
	}
	return nil, fmt.Errorf("microsoft graph returned more than %d pages of groups", entraGraphMaxPages)
}
