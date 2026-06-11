package control

import (
	"context"

	"reasonix/internal/provider"
)

// scriptedTurns is a provider that replays a distinct chunk set per Stream call,
// so a controller turn that re-enters the agent sees a different model response
// each time.
type scriptedTurns struct {
	turns [][]provider.Chunk
	call  int
}

func (s *scriptedTurns) Name() string { return "scripted" }

func (s *scriptedTurns) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	i := s.call
	if i >= len(s.turns) {
		i = len(s.turns) - 1
	}
	s.call++
	ch := make(chan provider.Chunk, len(s.turns[i]))
	for _, c := range s.turns[i] {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func firstUserMessage(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			return m.Content
		}
	}
	return ""
}

func textTurn(text string) []provider.Chunk {
	return []provider.Chunk{{Type: provider.ChunkText, Text: text}, {Type: provider.ChunkDone}}
}
