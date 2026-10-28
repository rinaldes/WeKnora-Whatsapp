package whatsapp

import (
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

type presenceLog struct {
	mu     sync.Mutex
	states []types.ChatPresence
}

func (p *presenceLog) send(_ types.JID, s types.ChatPresence) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.states = append(p.states, s)
}

func (p *presenceLog) get() []types.ChatPresence {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]types.ChatPresence(nil), p.states...)
}

func countState(states []types.ChatPresence, s types.ChatPresence) int {
	n := 0
	for _, x := range states {
		if x == s {
			n++
		}
	}
	return n
}

var testChat = types.NewJID("6281111111111", types.DefaultUserServer)

func TestTypingStartStop(t *testing.T) {
	var log presenceLog
	tl := newTypingLoops(log.send)
	tl.interval = 10 * time.Millisecond

	tl.Start(testChat)
	time.Sleep(35 * time.Millisecond)
	tl.Stop(testChat)

	states := log.get()
	if countState(states, types.ChatPresenceComposing) < 2 {
		t.Fatalf("composing not repeated: %v", states)
	}
	if states[len(states)-1] != types.ChatPresencePaused {
		t.Fatalf("last state = %v, want paused", states)
	}
	n := len(states)
	time.Sleep(30 * time.Millisecond)
	if len(log.get()) != n {
		t.Fatal("loop kept running after Stop")
	}
	tl.Stop(testChat) // no loop: no-op
}

func TestTypingTimeout(t *testing.T) {
	var log presenceLog
	tl := newTypingLoops(log.send)
	tl.timeout = 20 * time.Millisecond

	tl.Start(testChat)
	time.Sleep(60 * time.Millisecond)
	states := log.get()
	if len(states) != 2 || states[1] != types.ChatPresencePaused {
		t.Fatalf("states = %v, want composing then paused", states)
	}
	tl.mu.Lock()
	running := len(tl.loops)
	tl.mu.Unlock()
	if running != 0 {
		t.Fatal("timed-out loop still registered")
	}
}

func TestTypingRestartResetsTimeout(t *testing.T) {
	var log presenceLog
	tl := newTypingLoops(log.send)
	tl.timeout = 40 * time.Millisecond

	tl.Start(testChat)
	time.Sleep(25 * time.Millisecond)
	tl.Start(testChat) // reset
	time.Sleep(25 * time.Millisecond)
	if countState(log.get(), types.ChatPresencePaused) != 0 {
		t.Fatal("timeout was not reset")
	}
	tl.StopAll()
}
