package bot

import (
	"testing"
	"time"

	"reasonix/internal/control"
)

type closeProbeBotController struct {
	*control.Controller
	onClose func()
}

func (c *closeProbeBotController) Close() {
	if c.onClose != nil {
		c.onClose()
	}
}

func TestBotGatewayStopClosesSessionsWithoutGatewayLock(t *testing.T) {
	gw := &BotGateway{
		controllers: map[string]*sessionState{},
	}
	closed := make(chan struct{}, 1)
	gw.controllers["session"] = &sessionState{
		ctrl: &closeProbeBotController{
			Controller: control.New(control.Options{}),
			onClose: func() {
				gw.mu.Lock()
				gw.mu.Unlock()
				closed <- struct{}{}
			},
		},
	}

	done := make(chan struct{})
	go func() {
		gw.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked while closing a controller")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("controller Close was not called")
	}
	if len(gw.controllers) != 0 {
		t.Fatalf("controllers retained after Stop: %d", len(gw.controllers))
	}
}
