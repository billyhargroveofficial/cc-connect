package bots

import "fmt"

// CodexConnectionOptions is the transport-only configuration shared by bot
// sessions, discovery, explicit Stop and the isolated daily inventory agent.
// Instructions, tools, skills and per-bot session settings never cross here.
func (r *Runtime) CodexConnectionOptions() (map[string]any, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: a dedicated Codex app-server connection is required", ErrInvalid)
	}
	configured := r.cfg.AgentOptions["codex"]
	rawURL, _ := configured["app_server_url"].(string)
	endpoint, err := ValidateCodexAppServerURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: dedicated Codex app-server connection: %v", ErrInvalid, err)
	}
	connection := map[string]any{"backend": "app-server", "app_server_url": endpoint}
	for _, key := range []string{"cmd", "cli_path", "codex_home"} {
		if value, ok := configured[key]; ok {
			connection[key] = value
		}
	}
	if configured["env"] != nil {
		connection["env"] = runtimeEnv(configured["env"])
	}
	return connection, nil
}

func (r *Runtime) applyCodexConnection(options map[string]any) error {
	connection, err := r.CodexConnectionOptions()
	if err != nil {
		return err
	}
	// Product transport remains authoritative after per-bot skill options have
	// merged. No workspace setting can redirect a bot to the shared daemon.
	for _, key := range []string{"backend", "app_server_url", "cmd", "cli_path", "codex_home", "env"} {
		delete(options, key)
	}
	mergeOptions(options, connection)
	return nil
}
