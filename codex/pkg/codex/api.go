package codex

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const defaultAPIKeyEnv = "C2OPS_CODEX_API_KEY"

// APIConfig selects an external Responses API. It references a credential by
// environment variable name so recipes and command arguments need no secret.
type APIConfig struct {
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func normalizeAPI(input *APIConfig, env map[string]string) (*APIConfig, error) {
	if input == nil {
		return nil, nil
	}
	api := *input
	api.BaseURL = strings.TrimRight(strings.TrimSpace(api.BaseURL), "/")
	parsed, err := url.Parse(api.BaseURL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(api.BaseURL, "#") {
		// Do not echo the URL: a mistaken credential in it must not reach logs.
		return nil, fmt.Errorf("api.base_url must be an absolute HTTP(S) API base URL without credentials, query, or fragment")
	}
	api.APIKeyEnv = strings.TrimSpace(api.APIKeyEnv)
	if api.APIKeyEnv == "" {
		api.APIKeyEnv = defaultAPIKeyEnv
	}
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
	return []string{
		"-c", `model_provider="c2ops_external"`,
		"-c", `model_providers.c2ops_external={name="External API",wire_api="responses",requires_openai_auth=false,supports_websockets=false,base_url=` + strconv.Quote(api.BaseURL) + `,env_key=` + strconv.Quote(api.APIKeyEnv) + `}`,
	}
}
