// QA Author — adversarial tests
// Break-It dimensions covered: a real unix socket round trip (not net.Pipe),
// a peer that closes without a trailing newline, a peer sending garbage, a
// peer that never replies (ctx deadline), a ctx cancelled with no deadline
// (the context.AfterFunc close path), and a runaway peer past the maxReply
// cap.
package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// realSocket starts a real unix-domain listener and hands each accepted
// connection to serve, running in its own goroutine. It uses
// os.MkdirTemp("", "hd") rather than t.TempDir(): the latter nests under a
// path keyed by the test name, which routinely blows past macOS's ~104-byte
// sun_path limit for unix sockets.
func realSocket(t *testing.T, serve func(net.Conn)) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "hd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		connCh <- conn
		defer conn.Close()
		serve(conn)
	}()
	// However serve() ends (return, or stuck blocked on a Write/Read to a
	// peer that stopped draining), force the connection closed once the test
	// itself is done so nothing leaks past it.
	t.Cleanup(func() {
		select {
		case conn := <-connCh:
			conn.Close()
		default:
		}
	})
	return NewClient(path)
}

func readReq(t *testing.T, conn net.Conn) map[string]any {
	t.Helper()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Errorf("server: read request: %v", err)
		return nil
	}
	var req map[string]any
	if err := json.Unmarshal(line, &req); err != nil {
		t.Errorf("server: bad request: %v", err)
	}
	return req
}

func TestClientRealUnixSocketRoundTrip(t *testing.T) {
	c := realSocket(t, func(conn net.Conn) {
		req := readReq(t, conn)
		if req["method"] != "ping" {
			t.Errorf("method = %v", req["method"])
		}
		conn.Write([]byte(`{"id":"x","result":{"type":"pong","version":"0.9.0","protocol":22}}` + "\n"))
	})
	p, err := c.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Protocol != 22 || p.Version != "0.9.0" {
		t.Fatalf("pong = %+v", p)
	}

	c2 := realSocket(t, func(conn net.Conn) {
		req := readReq(t, conn)
		if req["method"] != "agent.list" {
			t.Errorf("method = %v", req["method"])
		}
		conn.Write([]byte(`{"id":"y","result":{"agents":[{"pane_id":"p1","agent_status":"working"}]}}` + "\n"))
	})
	agents, err := c2.AgentList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].PaneID != "p1" || agents[0].Status != StatusWorking {
		t.Fatalf("agents = %+v", agents)
	}
}

// herdr closes right after its reply; a final line with no trailing newline
// is still a complete reply (this is the client's EOF-tolerance path, now
// exercised over a real socket rather than net.Pipe).
func TestClientRealSocketNoTrailingNewlineThenClose(t *testing.T) {
	c := realSocket(t, func(conn net.Conn) {
		readReq(t, conn)
		conn.Write([]byte(`{"id":"x","result":{"agents":[]}}`)) // no trailing '\n', then close
	})
	agents, err := c.AgentList(context.Background())
	if err != nil {
		t.Fatalf("no trailing newline: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("agents = %+v, want none", agents)
	}
}

func TestClientRealSocketGarbageReply(t *testing.T) {
	c := realSocket(t, func(conn net.Conn) {
		readReq(t, conn)
		conn.Write([]byte("\x00\x01not-json-at-all\n"))
	})
	if _, err := c.AgentList(context.Background()); err == nil {
		t.Fatal("garbage reply over a real socket must error")
	}
}

// A peer that accepts the connection and reads the request but never
// replies must be bounded by the call's context deadline, not hang forever.
func TestClientRealSocketNeverRepliesTimesOut(t *testing.T) {
	block := make(chan struct{})
	c := realSocket(t, func(conn net.Conn) {
		readReq(t, conn)
		<-block // hold the connection open past the client's deadline
	})
	t.Cleanup(func() { close(block) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.AgentList(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

// A ctx with no deadline (context.WithCancel) relies solely on the
// context.AfterFunc(ctx, conn.Close) path in Call to unblock a pending read
// when cancelled — conn.SetDeadline is never reached because ctx.Deadline()
// is not ok. This must unblock promptly on cancel, not hang.
func TestClientRealSocketCancelWithoutDeadlineUnblocks(t *testing.T) {
	received := make(chan struct{})
	block := make(chan struct{})
	c := realSocket(t, func(conn net.Conn) {
		readReq(t, conn)
		close(received)
		<-block
	})
	t.Cleanup(func() { close(block) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.AgentList(ctx)
		done <- err
	}()

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("server never saw the request")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not unblock on cancel without a deadline")
	}
}

// A runaway peer that keeps sending past maxReply without ever sending a
// newline must not be read forever: the client is bounded by the maxReply
// cap and fails fast with a decode error, rather than exhausting the whole
// (generous) context deadline.
func TestClientRealSocketRunawayReplyStopsAtCap(t *testing.T) {
	stop := make(chan struct{})
	c := realSocket(t, func(conn net.Conn) {
		readReq(t, conn)
		chunk := bytes.Repeat([]byte{'x'}, 64<<10) // 64KiB, never a newline
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
	})
	t.Cleanup(func() { close(stop) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.AgentList(ctx)
	if err == nil {
		t.Fatal("a runaway peer past maxReply must error, not decode")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("client read past the maxReply cap instead of stopping at it: %v", err)
	}
}
