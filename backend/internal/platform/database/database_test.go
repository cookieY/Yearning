package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeTx struct {
	committed   int
	rolledBack  int
	commitErr   error
	rollbackErr error
}

func (f *fakeTx) Commit(ctx context.Context) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	f.committed++
	return nil
}

func (f *fakeTx) Rollback(ctx context.Context) error {
	if f.rollbackErr != nil {
		return f.rollbackErr
	}
	f.rolledBack++
	return nil
}

type fakeConnector struct {
	begins   int
	beginErr error
	lastTx   *fakeTx
	pingErr  error
	closeErr error
}

func (f *fakeConnector) Begin(ctx context.Context) (Tx, error) {
	f.begins++
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	tx := &fakeTx{}
	f.lastTx = tx
	return tx, nil
}

func (f *fakeConnector) Ping(ctx context.Context) error { return f.pingErr }
func (f *fakeConnector) Close() error                   { return f.closeErr }

func TestWithinTxCommitsOnSuccess(t *testing.T) {
	conn := &fakeConnector{}
	m := NewTxManager(conn)
	err := m.WithinTx(context.Background(), func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.begins != 1 {
		t.Fatalf("begins = %d, want 1", conn.begins)
	}
	if conn.lastTx.committed != 1 || conn.lastTx.rolledBack != 0 {
		t.Fatalf("committed=%d rolledBack=%d, want 1/0", conn.lastTx.committed, conn.lastTx.rolledBack)
	}
}

func TestWithinTxRollsBackOnError(t *testing.T) {
	conn := &fakeConnector{}
	m := NewTxManager(conn)
	sentinel := errors.New("business failure")
	err := m.WithinTx(context.Background(), func(ctx context.Context) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
	if conn.lastTx.committed != 0 || conn.lastTx.rolledBack != 1 {
		t.Fatalf("committed=%d rolledBack=%d, want 0/1", conn.lastTx.committed, conn.lastTx.rolledBack)
	}
}

func TestWithinTxNestedReusesTransaction(t *testing.T) {
	conn := &fakeConnector{}
	m := NewTxManager(conn)
	err := m.WithinTx(context.Background(), func(ctx context.Context) error {
		return m.WithinTx(ctx, func(ctx context.Context) error { return nil })
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.begins != 1 {
		t.Fatalf("nested WithinTx must reuse tx: begins=%d", conn.begins)
	}
	if conn.lastTx.committed != 1 {
		t.Fatal("outer tx must commit exactly once")
	}
}

func TestWithinTxInnerErrorRollsBackOuter(t *testing.T) {
	conn := &fakeConnector{}
	m := NewTxManager(conn)
	sentinel := errors.New("inner failure")
	err := m.WithinTx(context.Background(), func(ctx context.Context) error {
		return m.WithinTx(ctx, func(ctx context.Context) error { return sentinel })
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sentinel", err)
	}
	if conn.lastTx.rolledBack != 1 {
		t.Fatal("outer tx must roll back after inner failure")
	}
}

func TestWithinTxBeginFailurePropagates(t *testing.T) {
	conn := &fakeConnector{beginErr: errors.New("pool exhausted")}
	m := NewTxManager(conn)
	err := m.WithinTx(context.Background(), func(ctx context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "begin transaction") {
		t.Fatalf("begin failure must propagate with context: %v", err)
	}
}

func TestWithinTxRollbackFailureReported(t *testing.T) {
	tx := &fakeTx{rollbackErr: errors.New("connection lost")}
	m := NewTxManager(&beginFixedConnector{tx: tx})
	err := m.WithinTx(context.Background(), func(ctx context.Context) error { return errors.New("biz") })
	if err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback failure must surface: %v", err)
	}
}

func TestWithinTxPanicRollsBackAndPropagates(t *testing.T) {
	conn := &fakeConnector{}
	m := NewTxManager(conn)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("panic must propagate")
		}
		if conn.lastTx.rolledBack != 1 || conn.lastTx.committed != 0 {
			t.Fatalf("panic must roll back: rolledBack=%d committed=%d", conn.lastTx.rolledBack, conn.lastTx.committed)
		}
	}()
	_ = m.WithinTx(context.Background(), func(ctx context.Context) error {
		panic("boom")
	})
}

type beginFixedConnector struct{ tx Tx }

func (b *beginFixedConnector) Begin(ctx context.Context) (Tx, error) { return b.tx, nil }
func (b *beginFixedConnector) Ping(ctx context.Context) error        { return nil }
func (b *beginFixedConnector) Close() error                          { return nil }
