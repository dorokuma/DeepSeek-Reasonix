package provider_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"reasonix/internal/provider"
)

func TestChunkRingCloseDeadlock(t *testing.T) {
	// 场景：消费者提前退出，buffer 有残 chunk。
	// 使用可取消的 context 模拟真实场景：当消费者退出时 context 应被取消。

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan provider.Chunk, 1) // buffer=1 容易触发阻塞

	cr := provider.NewChunkRing(ctx, out, 256)

	// 塞满 buffer，让 drain goroutine 忙于发送
	for i := 0; i < 300; i++ {
		cr.Send(ctx, provider.Chunk{Type: provider.ChunkText, Text: fmt.Sprintf("chunk-%d", i)})
	}

	// 模拟消费者退出：不读 out channel，取消 context
	cancel()

	// 给一点时间让 drain goroutine 跑起来
	time.Sleep(50 * time.Millisecond)

	// 调用 Close——如果死锁，这会在 5 秒后 timeout
	done := make(chan bool)
	go func() {
		cr.Close()
		done <- true
	}()

	select {
	case <-done:
		t.Log("Close returned successfully")
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: Close() did not return within 5s")
	}
}
