package picker

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshPreservesSearchAndFocusedChoice(t *testing.T) {
	m := openSync(threeItems())
	cmd := m.Open(func(context.Context) ([]Item, error) {
		return append([]Item{{ID: "new", Label: "New model"}}, threeItems()...), nil
	})
	require.True(t, m.Refreshing())
	require.False(t, m.Loading())
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	require.Equal(t, "bravo", m.CursorID())
	m, _ = m.Update(cmd())
	assert.Equal(t, "b", m.filter)
	assert.Equal(t, "bravo", m.CursorID())
	assert.False(t, m.Refreshing())
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	assert.Contains(t, m.View(), "New model")
}

func TestRefreshFailureKeepsChoicesSelectableAndCanRetry(t *testing.T) {
	m := openSync(threeItems())
	m.SetRefreshInterval(time.Nanosecond)
	calls := 0
	cmd := m.Open(func(context.Context) ([]Item, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("offline")
		}
		return threeItems(), nil
	})
	m, timer := m.Update(cmd())
	require.NotNil(t, timer, "failed refresh should still schedule recovery")
	require.NoError(t, m.LoadErr())
	require.Error(t, m.RefreshErr())
	assert.Contains(t, m.View(), "Alpha")
	assert.Contains(t, m.View(), "refresh failed")
	assert.Contains(t, m.View(), "Ctrl+R retry")
	selectable := m
	_, selectCmd := selectable.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, selectCmd)
	assert.Equal(t, "alpha", selectCmd().(SelectMsg).Item.ID)

	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	require.NotNil(t, cmd)
	m, _ = m.Update(cmd())
	assert.NoError(t, m.RefreshErr())
	assert.Equal(t, 2, calls)
}

func TestPeriodicRefreshAddsModelsWithoutOverlappingRequests(t *testing.T) {
	m := openSync(threeItems())
	m.SetRefreshInterval(time.Nanosecond)
	calls := 0
	cmd := m.Open(func(context.Context) ([]Item, error) {
		calls++
		if calls == 1 {
			return threeItems(), nil
		}
		return append(threeItems(), Item{ID: "new", Label: "New model"}), nil
	})
	m, timer := m.Update(cmd())
	require.NotNil(t, timer)
	tick := timer()
	m, cmd = m.Update(tick)
	require.NotNil(t, cmd)
	require.True(t, m.Refreshing())
	_, duplicate := m.Update(tick)
	assert.Nil(t, duplicate)
	_, manual := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	assert.Nil(t, manual, "manual refresh must not overlap an active request")
	m, timer = m.Update(cmd())
	assert.Contains(t, m.View(), "New model")
	assert.Equal(t, 2, calls)
	require.NotNil(t, timer, "periodic refresh should continue after success")
	_, obsolete := m.Update(tick)
	assert.Nil(t, obsolete, "an old tick must not start another request")
}

func TestPickerIgnoresRepliesAndTimersFromPreviousOpen(t *testing.T) {
	old := openSync(threeItems())
	old.SetRefreshInterval(time.Nanosecond)
	cmd := old.Open(func(context.Context) ([]Item, error) { return threeItems(), nil })
	oldReply := cmd()
	old, timer := old.Update(oldReply)
	require.NotNil(t, timer)
	oldTick := timer()
	old.Hide()
	m := New("test", "New picker")
	m.Resize(80, 24)
	newCmd := m.Open(func(context.Context) ([]Item, error) {
		return []Item{{ID: "fresh", Label: "Fresh model"}}, nil
	})
	m, ignored := m.Update(oldReply)
	assert.Nil(t, ignored)
	assert.True(t, m.Loading())
	m, ignored = m.Update(oldTick)
	assert.Nil(t, ignored)
	m, _ = m.Update(newCmd())
	assert.Equal(t, "fresh", m.CursorID())
	assert.NotContains(t, m.View(), "Alpha")
}

func TestClosingPickerCancelsInFlightRefresh(t *testing.T) {
	m := openSync(threeItems())
	started := make(chan struct{})
	result := make(chan tea.Msg, 1)
	cmd := m.Open(func(ctx context.Context) ([]Item, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	defer m.Hide()
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh never started")
	}
	m.Hide()
	select {
	case msg := <-result:
		assert.ErrorIs(t, msg.(itemsLoadedMsg).err, context.Canceled)
		m, next := m.Update(msg)
		assert.Nil(t, next)
		assert.False(t, m.Visible())
	case <-time.After(time.Second):
		t.Fatal("closing did not cancel the refresh")
	}
}
