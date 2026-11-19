package main

import (
	"context"
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// guiHistory adapts the session checkpoint API to gui.TurnHistory. It is used
// by both the native window and (when injected) the optional web bridge, so
// undo/retry behave identically on every frontend.
type guiHistory struct {
	sessions string
	root     string
}

func (h *guiHistory) Undo(ctx context.Context, sid string, steps int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if steps < 1 {
		steps = 1
	}
	sess, err := session.OpenSession(h.sessions, sid)
	if err != nil {
		return err
	}
	checkpoint, _, err := sess.CheckpointBack(steps)
	if err != nil {
		return err
	}
	_, err = session.UndoCheckpoint(h.root, checkpoint)
	return err
}

func (h *guiHistory) Retry(ctx context.Context, sid string, steps int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if steps < 1 {
		steps = 1
	}
	sess, err := session.OpenSession(h.sessions, sid)
	if err != nil {
		return err
	}
	_, record, err := sess.CheckpointBack(steps)
	if err != nil {
		return err
	}
	fork, err := sess.RetryFromCheckpoint(record.ID)
	if err != nil {
		return err
	}
	fmt.Printf("forked session %s\n", fork.ID())
	return nil
}
