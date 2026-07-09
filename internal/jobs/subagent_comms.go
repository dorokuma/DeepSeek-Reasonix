package jobs

import (
	"context"
	"log/slog"
)

// JobFromContext returns the background job stamped on ctx (task sub-agent run).
func JobFromContext(ctx context.Context) (*Job, bool) {
	j, ok := ctx.Value(jobKey{}).(*Job)
	return j, ok && j != nil
}

// DrainJobSteer returns one queued steer-job message for this sub-agent, if any.
func DrainJobSteer(ctx context.Context) string {
	j, ok := JobFromContext(ctx)
	if !ok {
		return ""
	}
	select {
	case msg := <-j.steerCh:
		return msg
	default:
		return ""
	}
}

// RecordAck records that the sub-agent consumed a parent steer (snapshot for peek-job).
func RecordAck(ctx context.Context, msg string) {
	j, ok := JobFromContext(ctx)
	if !ok || msg == "" {
		return
	}
	n := JobNotify{JobID: j.ID, Type: "ack", AckMsg: msg}
	j.mu.Lock()
	j.lastAck = msg
	j.mu.Unlock()
	nonblockSendAck(j, n)
}

// RecordProgress records sub-agent step progress (snapshot + notifyCh, may drop).
func RecordProgress(ctx context.Context, step int, lastTool string) {
	j, ok := JobFromContext(ctx)
	if !ok {
		return
	}
	n := JobNotify{JobID: j.ID, Type: "progress", Step: step, LastTool: lastTool}
	j.mu.Lock()
	j.step = step
	j.lastTool = lastTool
	j.mu.Unlock()
	nonblockSendProgress(j, n)
}

func nonblockSendAck(j *Job, n JobNotify) {
	select {
	case j.ackCh <- n:
	default:
		slog.Debug("ackCh full, dropping ack", "job", j.ID)
	}
}

func nonblockSendProgress(j *Job, n JobNotify) {
	select {
	case j.notifyCh <- n:
	default:
		slog.Debug("notifyCh full, dropping progress", "job", j.ID)
	}
}
