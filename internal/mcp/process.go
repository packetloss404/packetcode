package mcp

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/packetcode/packetcode/internal/config"
)

// spawnServerProcess starts the MCP server child described by cfg, wires
// up stdin/stdout pipes, opens the per-server log file under logDir, and
// launches a goroutine that tees stderr into that log file. The caller
// owns the returned cmd / pipes / log file and must close them.
//
// The child receives a small allowlist of process-launch environment
// variables overlaid with cfg.Env (cfg wins on key collision). MCP
// servers are configured local code, but they do not need packetcode's
// full provider/API-key environment by default.
func spawnServerProcess(cfg ServerConfig, logDir string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, *os.File, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = serverEnv(os.Environ(), cfg.Env, cfg.EnvFrom)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := os.MkdirAll(logDir, 0o700); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, nil, nil, nil, fmt.Errorf("create log dir: %w", err)
	}
	logFileName, err := config.MCPLogFileName(cfg.Name)
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, nil, nil, nil, err
	}
	logPath := filepath.Join(logDir, logFileName)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, nil, nil, nil, fmt.Errorf("open log file: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = logFile.Close()
		return nil, nil, nil, nil, fmt.Errorf("start: %w", err)
	}

	// Stderr-tee runs until the child closes its stderr (typically on
	// exit). io.Copy returning is also our signal that no more
	// diagnostics are coming.
	go func() {
		_, _ = io.Copy(logFile, stderr)
	}()

	return cmd, stdin, stdout, logFile, nil
}

var inheritedMCPEnvKeys = map[string]struct{}{
	"APPDATA":             {},
	"COMSPEC":             {},
	"HOME":                {},
	"HTTPS_PROXY":         {},
	"HTTP_PROXY":          {},
	"LANG":                {},
	"LC_ALL":              {},
	"LOCALAPPDATA":        {},
	"NO_PROXY":            {},
	"NODE_EXTRA_CA_CERTS": {},
	"PATH":                {},
	"PATHEXT":             {},
	"SSL_CERT_DIR":        {},
	"SSL_CERT_FILE":       {},
	"SYSTEMROOT":          {},
	"TEMP":                {},
	"TMP":                 {},
	"USERPROFILE":         {},
	"WINDIR":              {},
	"XDG_CACHE_HOME":      {},
	"XDG_CONFIG_HOME":     {},
	"XDG_DATA_HOME":       {},
	"XDG_STATE_HOME":      {},
	"http_proxy":          {},
	"https_proxy":         {},
	"no_proxy":            {},
}

func serverEnv(processEnv []string, configured map[string]string, envFrom []string) []string {
	return mergeEnv(mergeEnv(filterInheritedEnv(processEnv), selectedEnv(processEnv, envFrom)), configured)
}

func filterInheritedEnv(env []string) []string {
	out := make([]string, 0, len(inheritedMCPEnvKeys))
	seen := map[string]struct{}{}
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		lookup := key
		if runtime.GOOS == "windows" {
			lookup = strings.ToUpper(key)
		}
		if _, keep := inheritedMCPEnvKeys[lookup]; !keep {
			continue
		}
		if _, dup := seen[envKeyID(key)]; dup {
			continue
		}
		seen[envKeyID(key)] = struct{}{}
		out = append(out, kv)
	}
	return out
}

// mergeEnv overlays overlay onto base. Base entries are "KEY=VALUE"
// strings. Keys present in overlay replace any matching entry in base.
func mergeEnv(base []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		return base
	}
	idx := map[string]int{}
	for i, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if ok {
			idx[envKeyID(key)] = i
		}
	}
	out := make([]string, len(base))
	copy(out, base)
	for k, v := range overlay {
		entry := k + "=" + v
		if i, ok := idx[envKeyID(k)]; ok {
			out[i] = entry
		} else {
			out = append(out, entry)
		}
	}
	return out
}

func selectedEnv(processEnv []string, names []string) map[string]string {
	if len(names) == 0 {
		return nil
	}
	want := map[string]string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		want[envKeyID(name)] = name
	}
	if len(want) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, kv := range processEnv {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if configuredName, keep := want[envKeyID(key)]; keep {
			out[configuredName] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func envKeyID(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}
