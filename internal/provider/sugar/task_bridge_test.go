package sugar

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/provider"
)

func TestTaskBridgeEndpointIsProcessScopedAndFailsClosed(t *testing.T) {
	root := "http://127.0.0.1:3218/task/36dcc4a4-e386-4b1a-882c-dfb58d68a20c/api/v1"
	got, err := taskBridgeBaseURL(root)
	require.NoError(t, err)
	require.Equal(t, root, got)
	for _, raw := range []string{"https://example.com/api/v1", "http://127.0.0.1:3000/task/36dcc4a4-e386-4b1a-882c-dfb58d68a20c/api/v1", root + "?key=secret", root + "/../v1"} {
		_, err := taskBridgeBaseURL(raw)
		require.Error(t, err)
	}
	t.Setenv("SUGAR_TASK_API_ROOT", "https://example.com/api/v1")
	p := NewWithBaseURL("https://usesugar.dev/api/v1", "existing-sign-in")
	_, err = p.ListModels(context.Background())
	require.ErrorContains(t, err, "SUGAR_TASK_API_ROOT")
	_, err = p.ChatCompletion(context.Background(), provider.ChatRequest{})
	require.ErrorContains(t, err, "SUGAR_TASK_API_ROOT")
}
