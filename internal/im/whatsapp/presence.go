package whatsapp

import (
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

const (
	// typingInterval re-sends "composing"; WhatsApp clears it after ~10 s.
	typingInterval = 8 * time.Second
	// typingTimeout ends a loop whose reply never came (dedup, ignored
	// commands, failures), so "typing…" can't stay on forever.
	typingTimeout = 5 * time.Minute
)

// typingLoops keeps one "typing…" loop per chat.
type typingLoops struct {
	send     func(chat types.JID, state types.ChatPresence)
	interval time.Duration
	timeout  time.Duration

	mu    sync.Mutex
	loops map[types.JID]*typingLoop
}

type typingLoop struct {
	reset chan struct{}
	stop  chan struct{}
	done  chan struct{}
}

func newTypingLoops(send func(chat types.JID, state types.ChatPresence)) *typingLoops {
	return &typingLoops{
		send:     send,
		interval: typingInterval,
		timeout:  typingTimeout,
		loops:    make(map[types.JID]*typingLoop),
	}
}

// Start shows "typing…" in chat until Stop, StopAll or the timeout. Starting
// a chat that is already typing resets its timeout.
func (t *typingLoops) Start(chat types.JID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if l, ok := t.loops[chat]; ok {
		select {
		case l.reset <- struct{}{}:
		default:
		}
		return
	}
	l := &typingLoop{
		reset: make(chan struct{}, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	t.loops[chat] = l
	go t.run(chat, l)
}

// Stop ends the chat's loop, if any, and waits until "paused" has been sent.
func (t *typingLoops) Stop(chat types.JID) {
	t.mu.Lock()
	l, ok := t.loops[chat]
	if ok {
		delete(t.loops, chat)
		close(l.stop)
	}
	t.mu.Unlock()
	if ok {
		<-l.done
	}
}

// StopAll ends every loop without waiting.
func (t *typingLoops) StopAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for chat, l := range t.loops {
		delete(t.loops, chat)
		close(l.stop)
	}
}

func (t *typingLoops) run(chat types.JID, l *typingLoop) {
	defer close(l.done)
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	timer := time.NewTimer(t.timeout)
	defer timer.Stop()

	t.send(chat, types.ChatPresenceComposing)
	for {
		select {
		case <-ticker.C:
			t.send(chat, types.ChatPresenceComposing)
		case <-l.reset:
			timer.Reset(t.timeout) // Go ≥1.23 timers: no drain needed
		case <-timer.C:
			t.mu.Lock()
			if t.loops[chat] == l {
				delete(t.loops, chat)
			}
			t.mu.Unlock()
			t.send(chat, types.ChatPresencePaused)
			return
		case <-l.stop:
			t.send(chat, types.ChatPresencePaused)
			return
		}
	}
}
