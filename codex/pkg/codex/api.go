package codex

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const resolvedAPIKeyEnv = "C2OPS_CODEX_RESOLVED_API_KEY"

// APIConfig selects an external Responses API when both URL and credentials
// are available. c2j resolves APIKey from an x-secret input before invocation.
type APIConfig struct {
	BaseURL   string  `json:"base_url"`
	APIKey    *string `json:"api_key,omitempty"`
	APIKeyEnv string  `json:"api_key_env,omitempty"`
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func normalizeAPI(input *APIConfig, env map[string]string) (*APIConfig, error) {
	if input == nil {
		return nil, nil
	}
	api := *input
	api.BaseURL = strings.TrimRight(strings.TrimSpace(api.BaseURL), "/")
	api.APIKeyEnv = strings.TrimSpace(api.APIKeyEnv)
	// Missing config resolves to null (decoded as an empty string), and an
	// unavailable optional secret omits api_key. Neither should change Codex's
	// normal authentication/provider behavior or forward a partial credential.
	if api.BaseURL == "" || (api.APIKey == nil && api.APIKeyEnv == "") {
		return nil, nil
	}
	parsed, err := url.Parse(api.BaseURL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(api.BaseURL, "#") {
		// Do not echo the URL: a mistaken credential in it must not reach logs.
		return nil, fmt.Errorf("api.base_url must be an absolute HTTP(S) API base URL without credentials, query, or fragment")
	}
	if api.APIKey != nil && api.APIKeyEnv == "" {
		if strings.TrimSpace(*api.APIKey) == "" {
			return nil, fmt.Errorf("api.api_key must be nonempty when supplied")
		}
		return &api, nil
	}
	// An explicitly selected environment credential overrides the tenant secret
	// default. Do not forward an unused resolved key to the child process.
	api.APIKey = nil
	if !envNamePattern.MatchString(api.APIKeyEnv) {
		return nil, fmt.Errorf("api.api_key_env must be an environment variable name")
	}
	key, overridden := env[api.APIKeyEnv]
	if !overridden {
		key = os.Getenv(api.APIKeyEnv)
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("external API credential is missing: set %s in the worker environment", api.APIKeyEnv)
	}
	return &api, nil
}

func (api APIConfig) commandArgs() []string {
	// Keep the key in the environment, never in argv or persisted config. SSE
	// works with Responses-compatible proxies without requiring WebSocket support.
	provider := `model_providers.c2ops_external={name="External API",wire_api="responses",requires_openai_auth=false,supports_websockets=false,base_url=` + strconv.Quote(api.BaseURL)
	keyEnv := api.APIKeyEnv
	if api.APIKey != nil {
		keyEnv = resolvedAPIKeyEnv
	}
	if keyEnv != "" {
		provider += `,env_key=` + strconv.Quote(keyEnv)
	}
	return []string{
		"-c", `model_provider="c2ops_external"`,
		"-c", provider + "}",
	}
}
