package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/testwait"
)

type recoveryGateProvider struct {
	*scriptedProvider
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *recoveryGateProvider) ListModels(ctx context.Context) ([]provider.Model, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.scriptedProvider.ListModels(ctx)
}

func TestResubmitConcurrentRequestsStartOnlyOneSuccessor(t *testing.T) {
	dir := t.TempDir()
	seedAbandonedJob(t, dir, "ab901234", "recover once")
	p := &recoveryGateProvider{scriptedProvider: &scriptedProvider{turns: scriptedHello()}, entered: make(chan struct{}), release: make(chan struct{})}
	m, _ := newTestManager(t, p, func(c *Config) { c.JobsDir = dir })
	type result struct {
		snap Snapshot
		err  *SpawnError
	}
	first := make(chan result, 1)
	go func() { s, e := m.Resubmit("ab901234"); first <- result{s, e} }()
	select {
	case <-p.entered:
	case <-time.After(testwait.Timeout(time.Second)):
		close(p.release)
		t.Fatal("resubmit never reached validation")
	}
	_, secondErr := m.Resubmit("ab901234")
	close(p.release)
	var firstResult result
	select {
	case firstResult = <-first:
	case <-time.After(testwait.Timeout(time.Second)):
		t.Fatal("resubmit did not finish")
	}
	require.Nil(t, firstResult.err)
	require.NotNil(t, secondErr)
	require.Equal(t, "resubmit_in_progress", secondErr.Code)
	require.Len(t, m.List(), 2, "one recovered job and exactly one successor")
	old, _ := m.Get("ab901234")
	require.Equal(t, firstResult.snap.ID, old.ResubmittedAs)
}

func TestResubmitFailedValidationReleasesReservation(t *testing.T) {
	for _, failure := range []string{"provider", "persistence"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			seedAbandonedJob(t, dir, "ab912345", "try manually again")
			p := &scriptedProvider{turns: scriptedHello()}
			m, _ := newTestManager(t, p, func(c *Config) { c.JobsDir = dir })
			if failure == "provider" {
				p.listErr = errors.New("provider unavailable")
			} else {
				blocked := filepath.Join(t.TempDir(), "blocked")
				require.NoError(t, os.WriteFile(blocked, []byte("x"), 0600))
				m.cfg.JobsDir = blocked
			}
			_, err := m.Resubmit("ab912345")
			require.NotNil(t, err)
			old, _ := m.Get("ab912345")
			require.Empty(t, old.ResubmittedAs)
			require.Len(t, m.List(), 1)
			p.listErr = nil
			m.cfg.JobsDir = dir
			successor, err := m.Resubmit("ab912345")
			require.Nil(t, err)
			require.NotEmpty(t, successor.ID)
		})
	}
}

func TestRecoveredResubmittableSkipsOversizePrompt(t *testing.T) {
	m, _ := managerOverAbandoned(t, "ab923456", strings.Repeat("x", MaxResubmitPromptBytes+1))
	require.Empty(t, m.RecoveredResubmittable())
	require.Len(t, m.List(), 1, "the original remains available for manual inspection")
}
