package sugar

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var taskBridgePath = regexp.MustCompile("^/task/[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}/api/v1$")

// A process-only override keeps the member's existing sign-in and saved Sugar endpoint.
// An invalid override fails closed instead of silently running an untracked task.
func taskBridgeBaseURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("SUGAR_TASK_API_ROOT must be the loopback API root printed by Sugar's task bridge")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port <= 3050 || port > 65535 || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || !taskBridgePath.MatchString(parsed.Path) {
		return "", fmt.Errorf("SUGAR_TASK_API_ROOT must be the loopback API root printed by Sugar's task bridge")
	}
	return parsed.String(), nil
}
