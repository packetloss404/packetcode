package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type refreshProvider struct {
	fakeProvider
	list func(context.Context) ([]Model, error)
}

func (p *refreshProvider) ListModels(ctx context.Context) ([]Model, error) { return p.list(ctx) }

func TestRegistryRefreshModelsNewestRequestWins(t *testing.T) {
	r := NewRegistry()
	started, release := make(chan struct{}), make(chan struct{}, 1)
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	p := &refreshProvider{fakeProvider: fakeProvider{slug: "test"}}
	p.list = func(ctx context.Context) ([]Model, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
				return []Model{{ID: "old"}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []Model{{ID: "new"}}, nil
	}
	r.Register(p)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.RefreshModels(ctx, "test"); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first request never started")
	}
	models, err := r.RefreshModels(ctx, "test")
	require.NoError(t, err)
	models[0].ID = "changed by caller"
	release <- struct{}{}
	require.NoError(t, <-done)
	cached, ok := r.CachedModels("test")
	require.True(t, ok)
	assert.Equal(t, []Model{{ID: "new"}}, cached)
}

func TestRegistryRefreshCannotUndoCacheInvalidation(t *testing.T) {
	for _, mutation := range []string{"invalidate", "register", "replace cache"} {
		t.Run(mutation, func(t *testing.T) {
			r := NewRegistry()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &refreshProvider{fakeProvider: fakeProvider{slug: "test"}}
			p.list = func(context.Context) ([]Model, error) {
				switch mutation {
				case "invalidate":
					r.InvalidateCachedModels("test")
				case "register":
					r.Register(&fakeProvider{slug: "test"})
				case "replace cache":
					r.SetCachedModels("test", []Model{{ID: "newer"}})
				}
				return []Model{{ID: "obsolete"}}, nil
			}
			r.Register(p)
			_, err := r.RefreshModels(ctx, "test")
			require.NoError(t, err)
			models, ok := r.CachedModels("test")
			if mutation == "replace cache" {
				require.True(t, ok)
				assert.Equal(t, []Model{{ID: "newer"}}, models)
			} else {
				assert.False(t, ok)
			}
		})
	}
}

func TestRegistryCanceledRefreshKeepsPreviousCatalog(t *testing.T) {
	r := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &refreshProvider{fakeProvider: fakeProvider{slug: "test"}}
	p.list = func(context.Context) ([]Model, error) {
		cancel()
		return []Model{{ID: "new"}}, nil
	}
	r.Register(p)
	r.SetCachedModels("test", []Model{{ID: "previous"}})
	_, err := r.RefreshModels(ctx, "test")
	require.ErrorIs(t, err, context.Canceled)
	models, ok := r.CachedModels("test")
	require.True(t, ok)
	assert.Equal(t, []Model{{ID: "previous"}}, models)
}
