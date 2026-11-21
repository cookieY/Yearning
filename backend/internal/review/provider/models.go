// models.go fetches the OpenAI-compatible GET /models catalog for AI Provider setup forms under
// the same HTTPS-only, 4 MiB response cap, and sanitized rejection guarantees as review calls.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

type modelsListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

var modelsHTTPClient = NewHTTPClient()

// ListModels queries baseURL/models and returns deduplicated, sorted model identifiers.
func ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	u, err := ParseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(u.String(), "/") + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := modelsHTTPClient.Inner.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseByte+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxResponseByte {
		return nil, fmt.Errorf("provider: response exceeds 4MiB")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, upstreamSnapshot(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &statusError{code: resp.StatusCode, snapshot: upstreamSnapshot(body)}
	}
	var parsed modelsListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: models: %v", ErrSchemaInvalid, err)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out, nil
}
