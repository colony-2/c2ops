package codex

import (
	"os"
	"path/filepath"
	"strings"
)

// executionConfig describes the wrapper's launch settings, not Codex's merged
// configuration. opts must be validated and its private home prepared first.
// Only explicitly allowed public fields belong here: never serialize Options,
// argv, environment values, auth.json, or config.toml into an artifact.
func executionConfig(opts Options, requested *APIConfig) map[string]any {
	fields := map[string]any{
		"mode":                "default",
		"provider":            "codex_config_or_default",
		"credential_source":   "codex_authentication",
		"model_override":      strings.TrimSpace(opts.Model),
		"resuming":            strings.TrimSpace(opts.SessionID) != "",
		"auth_json_present":   regularFilePresent(filepath.Join(opts.CodexHome, "auth.json")),
		"config_toml_present": regularFilePresent(filepath.Join(opts.CodexHome, "config.toml")),
	}
	hasURL, hasKey, hasEnv := false, false, false
	if requested != nil {
		hasURL = strings.TrimRight(strings.TrimSpace(requested.BaseURL), "/") != ""
		hasKey = requested.APIKey != nil
		hasEnv = strings.TrimSpace(requested.APIKeyEnv) != ""
	}
	fields["input_presence"] = map[string]bool{"base_url": hasURL, "api_key": hasKey, "api_key_env": hasEnv}
	if opts.API == nil {
		switch {
		case requested == nil:
			fields["reason"] = "api_not_supplied_or_disabled"
		case !hasURL && !hasKey && !hasEnv:
			fields["reason"] = "missing_base_url_and_api_key"
		case !hasURL:
			fields["reason"] = "missing_base_url"
		default:
			fields["reason"] = "missing_api_key"
		}
		return fields
	}
	fields["mode"] = "external_api"
	fields["reason"] = "url_and_credentials_available"
	fields["provider"] = "c2ops_external"
	fields["base_url"] = opts.API.BaseURL
	fields["wire_api"] = "responses"
	fields["requires_openai_auth"] = false
	fields["supports_websockets"] = false
	keyEnv := opts.API.APIKeyEnv
	if opts.API.APIKey != nil {
		keyEnv = resolvedAPIKeyEnv
		fields["credential_source"] = "api.api_key"
	} else if _, overridden := opts.ExtraEnv[keyEnv]; overridden {
		fields["credential_source"] = "input.env"
	} else {
		fields["credential_source"] = "worker_environment"
	}
	fields["credential_env"] = keyEnv
	key, overridden := buildEnv(opts)[keyEnv]
	if !overridden {
		key = os.Getenv(keyEnv)
	}
	fields["credential_present"] = strings.TrimSpace(key) != ""
	return fields
}

func regularFilePresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
