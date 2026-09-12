package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// /loop runs a prompt or /command repeatedly. Two modes, mirroring Claude Code:
//   - interval: "/loop 5m <body>" re-runs <body> every 5m.
//   - self-paced: "/loop <body>" re-runs <body> after each turn finishes, until
//     the model signals done (emits LOOP_DONE) or the iteration cap is hit.
// The body may be a prompt or a /command (e.g. /loop 10m /cost), and because it
// runs as a normal turn it can fan out background agents via spawn_agent, or run
// a workflow, with no special handling here.

type loopMode int

const (
	loopInterval loopMode = iota
	loopSelfPaced
)

// loopDoneSentinel, when present in a self-paced loop's final assistant message,
// stops the loop. The body prompt is augmented to tell the model about it.
const loopDoneSentinel = "LOOP_DONE"

const (
	loopDecisionOpen  = "<packetcode-loop-decision>"
	loopDecisionClose = "</packetcode-loop-decision>"
)

type loopDecision struct {
	Version  int    `json:"version"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// selfPacedMaxIters caps self-paced loops so a model that never emits the
// sentinel can't run forever.
const selfPacedMaxIters = 25

type loopState struct {
	id string
	// seq is the creation order behind id ("loop3" → 3). loops is a map, so
	// /loop list has to sort by something; sorting the ids as strings would
	// put loop10 before loop2 once ten loops exist.
	seq        int
	mode       loopMode
	interval   time.Duration
	body       string
	iterations int
	maxIters   int // 0 = unbounded (interval loops)
	stopped    bool
}

type loopTickMsg struct{ id string }

func loopTick(id string, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return loopTickMsg{id: id} })
}

func (a *App) handleLoopCommand(args []string) (tea.Model, tea.Cmd) {
	if a.loops == nil {
		a.loops = map[string]*loopState{}
	}
	if len(args) == 0 {
		a.conversation.AppendSystem("usage: /loop [interval] <prompt|/command>  ·  /loop list  ·  /loop stop [id|all]")
		return a, nil
	}
	switch args[0] {
	case "list":
		a.conversation.AppendSystem(a.renderLoops())
		return a, nil
	case "stop":
		return a.stopLoop(args[1:])
	}

	mode, interval, body, err := parseLoopArgs(args)
	if err != nil {
		a.conversation.AppendSystem("loop: " + err.Error())
		return a, nil
	}

	a.loopSeq++
	ls := &loopState{
		id:       fmt.Sprintf("loop%d", a.loopSeq),
		seq:      a.loopSeq,
		mode:     mode,
		interval: interval,
		body:     body,
	}
	if mode == loopSelfPaced {
		ls.maxIters = selfPacedMaxIters
	}
	a.loops[ls.id] = ls

	if mode == loopInterval {
		a.conversation.AppendSystem(fmt.Sprintf("loop %s started (every %s): %s  ·  /loop stop %s", ls.id, interval, body, ls.id))
		// Run once now, then tick.
		return a, tea.Batch(a.runLoopBody(ls), loopTick(ls.id, interval))
	}
	a.conversation.AppendSystem(fmt.Sprintf("loop %s started (self-paced, ≤%d iters): %s  ·  /loop stop %s", ls.id, ls.maxIters, body, ls.id))
	return a, a.runLoopBody(ls)
}

// parseLoopArgs classifies args into an interval or self-paced loop. The first
// token is an interval when it parses as a Go duration ("30s", "5m", "2h").
func parseLoopArgs(args []string) (loopMode, time.Duration, string, error) {
	if len(args) == 0 {
		return 0, 0, "", fmt.Errorf("nothing to loop")
	}
	if d, err := time.ParseDuration(args[0]); err == nil {
		body := strings.TrimSpace(strings.Join(args[1:], " "))
		if body == "" {
			return 0, 0, "", fmt.Errorf("interval given but no prompt/command to run")
		}
		if d < time.Second {
			return 0, 0, "", fmt.Errorf("interval too small (minimum 1s)")
		}
		return loopInterval, d, body, nil
	}
	body := strings.TrimSpace(strings.Join(args, " "))
	return loopSelfPaced, 0, body, nil
}

// runLoopBody executes one iteration of a loop's body. If a turn is already
// streaming it queues the body (never overlaps). Slash-command bodies are
// dispatched; prompt bodies start a turn (self-paced turns are tagged so
// agentDoneMsg can re-run them).
func (a *App) runLoopBody(ls *loopState) tea.Cmd {
	ls.iterations++
	body := ls.body

	// A /command body: dispatch it directly (doesn't invoke the agent loop,
	// so self-paced re-running is driven here, not by agentDoneMsg).
	if cmd, cargs, ok := a.slashRegistry().Parse(body); ok {
		before := len(a.queuedInputs)
		_, teacmd := a.handleSlashCommand(cmd, cargs, body)
		for i := before; i < len(a.queuedInputs); i++ {
			if a.queuedInputs[i].SourceLoopID == "" {
				a.queuedInputs[i].SourceLoopID = ls.id
			}
		}
		return teacmd
	}

	// Built once, before the streaming check. The queued path used to skip
	// this, so a loop body typed mid-turn was queued as bare text: it claimed
	// no loop ownership, and it did not carry the iteration instruction that
	// tells the model how to declare the work finished. The loop then sat in
	// /loop list forever, doing nothing.
	opt := turnOptions{text: body, emitUser: true, sourceLoopID: ls.id}
	if ls.mode == loopSelfPaced {
		opt.loopID = ls.id
		opt.text = body + "\n\n[Loop iteration " + fmt.Sprint(ls.iterations) + ". If the task is complete and no further iterations are needed, end your reply with " + loopDecisionOpen + `{"version":1,"decision":"stop","reason":"brief reason"}` + loopDecisionClose + ". The legacy " + loopDoneSentinel + " sentinel is also accepted.]"
	}
	// The transcript shows the body the user asked to loop, not the body plus
	// the machine-readable stop protocol appended to it.
	opt.display = body

	if a.streaming {
		a.queueTurn(opt)
		return nil
	}
	_, teacmd := a.startTurnWith(opt)
	return teacmd
}

// onLoopTurnDone is called from the agentDoneMsg handler for a turn that a
// self-paced loop owns. It re-runs the body unless the loop was stopped, the
// model emitted the done sentinel, or the iteration cap was reached. Returns a
// tea.Cmd to continue (nil to stop the loop).
func (a *App) onLoopTurnDone() tea.Cmd {
	id := a.activeLoopID
	a.activeLoopID = ""
	ls, ok := a.loops[id]
	if !ok || ls.stopped {
		return nil
	}
	if stop, reason, ok := parseLoopDecision(a.lastAgentText); ok && stop {
		if reason == "" {
			reason = "model returned a structured stop decision"
		}
		a.finishLoop(ls, reason)
		return nil
	}
	if strings.Contains(a.lastAgentText, loopDoneSentinel) {
		a.finishLoop(ls, "model emitted the legacy done sentinel")
		return nil
	}
	if ls.maxIters > 0 && ls.iterations >= ls.maxIters {
		a.finishLoop(ls, fmt.Sprintf("reached %d iterations", ls.maxIters))
		return nil
	}
	return a.runLoopBody(ls)
}

// stopLoopAfterFailedTurn ends the self-paced loop that owned a turn which
// was cancelled or errored. Re-running would either ignore a Ctrl+C or hammer
// a provider that just refused.
func (a *App) stopLoopAfterFailedTurn() {
	id := a.activeLoopID
	a.activeLoopID = ""
	ls, ok := a.loops[id]
	if !ok || ls.stopped {
		return
	}
	a.finishLoop(ls, "turn was cancelled or failed; not re-running")
}

func parseLoopDecision(text string) (stop bool, reason string, ok bool) {
	open := strings.LastIndex(text, loopDecisionOpen)
	if open < 0 {
		return false, "", false
	}
	payloadStart := open + len(loopDecisionOpen)
	closeRel := strings.Index(text[payloadStart:], loopDecisionClose)
	if closeRel < 0 {
		return false, "", false
	}
	var decision loopDecision
	if err := json.Unmarshal(
		[]byte(strings.TrimSpace(text[payloadStart:payloadStart+closeRel])),
		&decision,
	); err != nil {
		return false, "", false
	}
	if decision.Version != 1 {
		return false, "", false
	}
	switch decision.Decision {
	case "stop":
		return true, strings.TrimSpace(decision.Reason), true
	case "continue":
		return false, strings.TrimSpace(decision.Reason), true
	default:
		return false, "", false
	}
}

func (a *App) finishLoop(ls *loopState, reason string) {
	ls.stopped = true
	delete(a.loops, ls.id)
	a.conversation.AppendSystem(fmt.Sprintf("loop %s finished — %s", ls.id, reason))
}

func (a *App) stopLoop(args []string) (tea.Model, tea.Cmd) {
	if len(a.loops) == 0 {
		a.conversation.AppendSystem("no active loops")
		return a, nil
	}
	target := "all"
	if len(args) > 0 {
		target = args[0]
	}
	if target == "all" {
		n := len(a.loops)
		for _, ls := range a.loops {
			ls.stopped = true
		}
		a.loops = map[string]*loopState{}
		a.activeLoopID = ""
		a.removeQueuedLoopTurns("")
		a.conversation.AppendSystem(fmt.Sprintf("stopped %d loop(s)", n))
		return a, nil
	}
	ls, ok := a.loops[target]
	if !ok {
		a.conversation.AppendSystem("no loop " + target)
		return a, nil
	}
	ls.stopped = true
	delete(a.loops, target)
	a.removeQueuedLoopTurns(target)
	if a.activeLoopID == target {
		a.activeLoopID = ""
	}
	a.conversation.AppendSystem("stopped loop " + target)
	return a, nil
}

// Stop must remove an iteration already queued behind unrelated work, too.
func (a *App) removeQueuedLoopTurns(id string) {
	kept := a.queuedInputs[:0]
	for _, q := range a.queuedInputs {
		owner := q.SourceLoopID
		if owner == "" {
			owner = q.LoopID
		}
		if owner != "" && (id == "" || owner == id) {
			continue
		}
		kept = append(kept, q)
	}
	clear(a.queuedInputs[len(kept):])
	a.queuedInputs = kept
	if len(kept) == 0 {
		a.queuePaused = false
	}
	a.refreshTopBar()
}

func (a *App) renderLoops() string {
	if len(a.loops) == 0 {
		return "no active loops"
	}
	// Map iteration order is randomised, so list in creation order instead:
	// otherwise two /loop list calls a second apart reshuffle the rows.
	ordered := make([]*loopState, 0, len(a.loops))
	for _, ls := range a.loops {
		ordered = append(ordered, ls)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].seq < ordered[j].seq })

	var b strings.Builder
	b.WriteString("Active loops:\n")
	for _, ls := range ordered {
		if ls.mode == loopInterval {
			fmt.Fprintf(&b, "  %s — every %s · %d run(s) · %s\n", ls.id, ls.interval, ls.iterations, ls.body)
		} else {
			fmt.Fprintf(&b, "  %s — self-paced · %d/%d iters · %s\n", ls.id, ls.iterations, ls.maxIters, ls.body)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
