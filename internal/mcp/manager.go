package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxParallelStartup bounds the number of concurrent server spawns
// during Manager.Start. Spec: 8.
const maxParallelStartup = 8

// shutdownExtraTimeout is added to the per-client Close timeout when
// computing the overall Manager.Shutdown deadline. Spec: 1s.
const shutdownExtraTimeout = time.Second

// Manager owns a fleet of MCP Clients and surfaces them to the rest of
// the app via a name-keyed map. It is safe for concurrent use after
// Start has returned.
type Manager struct {
	cfg     Config
	mu      sync.RWMutex
	clients map[string]*Client
	reports []StartupReport
}

// NewManager constructs a Manager. Start() must be called before
// Clients() / Client() will return anything useful.
func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg:     cfg,
		clients: map[string]*Client{},
	}
}

// Start spawns every configured server in parallel (max 8 concurrent),
// runs the MCP handshake on each, and returns a StartupReport per
// server in input order. Successful clients are stored on the manager.
//
// Start is intended to be called once during app startup; calling it
// again is allowed but will overwrite the cached clients/reports.
func (m *Manager) Start(ctx context.Context) []StartupReport {
	servers := m.cfg.Servers
	reports := make([]StartupReport, len(servers))
	clients := make([]*Client, len(servers))

	sem := make(chan struct{}, maxParallelStartup)
	var wg sync.WaitGroup
	for i, sc := range servers {
		i, sc := i, sc
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if !sc.Enabled {
				reports[i] = StartupReport{
					Name:    sc.Name,
					Status:  "disabled",
					Command: renderServerCommand(sc),
				}
				return
			}
			cli, err := NewClient(ctx, sc, m.cfg.LogDir, m.cfg.ClientInfo)
			if err != nil {
				reports[i] = StartupReport{
					Name:    sc.Name,
					Status:  "failed",
					Command: renderServerCommand(sc),
					Err:     err.Error(),
				}
				return
			}
			clients[i] = cli
			reports[i] = StartupReport{
				Name:      sc.Name,
				Status:    "running",
				ToolCount: len(cli.Tools()),
				PID:       cli.PID(),
				Command:   renderServerCommand(sc),
			}
		}()
	}
	wg.Wait()

	m.mu.Lock()
	oldClients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		if c != nil {
			oldClients = append(oldClients, c)
		}
	}
	m.reports = reports
	m.clients = map[string]*Client{}
	for _, c := range clients {
		if c != nil {
			m.clients[c.Name()] = c
		}
	}
	m.mu.Unlock()
	for _, c := range oldClients {
		_ = c.Close(2 * time.Second)
	}
	return reports
}

// Clients returns the live client list in alphabetic-by-name order.
// Dead clients are filtered out.
func (m *Manager) Clients() []*Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.clients))
	for n, c := range m.clients {
		if c != nil && c.IsAlive() {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]*Client, len(names))
	for i, n := range names {
		out[i] = m.clients[n]
	}
	return out
}

// Client returns the named client (alive or not) along with ok=false if
// the name was never registered.
func (m *Manager) Client(name string) (*Client, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.clients[name]
	return c, ok
}

// Reports returns the cached StartupReport slice from Start, adjusted
// for clients that have exited since startup. Returns a defensive copy.
func (m *Manager) Reports() []StartupReport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]StartupReport, len(m.reports))
	for i, r := range m.reports {
		if r.Status == "running" {
			c := m.clients[r.Name]
			if c == nil || !c.IsAlive() {
				r.Status = "exited"
				r.PID = -1
				if c != nil && c.DeathReason() != nil {
					r.Err = c.DeathReason().Error()
				}
			}
		}
		out[i] = r
	}
	return out
}

func renderServerCommand(sc ServerConfig) string {
	parts := make([]string, 0, 1+len(sc.Args))
	if sc.Command != "" {
		parts = append(parts, sc.Command)
	}
	parts = append(parts, sc.Args...)
	return strings.Join(parts, " ")
}

// Shutdown closes every alive client in parallel. Returns a composite
// error listing per-client failures, or nil if every client closed
// cleanly.
func (m *Manager) Shutdown(timeout time.Duration) error {
	m.mu.RLock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		if c != nil {
			clients = append(clients, c)
		}
	}
	m.mu.RUnlock()

	if len(clients) == 0 {
		return nil
	}

	type result struct {
		name string
		err  error
	}
	resCh := make(chan result, len(clients))
	for _, c := range clients {
		c := c
		go func() {
			err := c.Close(timeout)
			resCh <- result{name: c.Name(), err: err}
		}()
	}

	deadline := time.After(timeout + shutdownExtraTimeout)
	var errs []string
	collected := 0
	for collected < len(clients) {
		select {
		case r := <-resCh:
			collected++
			if r.err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", r.name, r.err))
			}
		case <-deadline:
			errs = append(errs, fmt.Sprintf("%d client(s) did not close within %s", len(clients)-collected, timeout+shutdownExtraTimeout))
			collected = len(clients) // bail
		}
	}
	if len(errs) > 0 {
		return errors.New("mcp.Shutdown: " + strings.Join(errs, "; "))
	}
	return nil
}
