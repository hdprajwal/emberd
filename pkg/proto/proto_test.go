package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	// A pair of connected pipes stands in for the vsock stream.
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	want := ExecRequest{Code: "print('hi')", Stdin: "data", TimeoutMs: 5000}

	go func() {
		if err := WriteMessage(client, want); err != nil {
			t.Errorf("WriteMessage: %v", err)
		}
	}()

	var got ExecRequest
	if err := ReadMessage(server, &got); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got != want {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestExecResultRoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		result     ExecResult
		wantUsWire bool // whether duration_us should appear in the JSON
	}{
		{
			name:       "both durations set",
			result:     ExecResult{Stdout: "out", Stderr: "err", ExitCode: 0, DurationMs: 12, DurationUs: 12847},
			wantUsWire: true,
		},
		{
			name:       "sub-millisecond has us but zero ms",
			result:     ExecResult{Stdout: "quick", ExitCode: 0, DurationMs: 0, DurationUs: 731},
			wantUsWire: true,
		},
		{
			name:       "zero us omitted from wire",
			result:     ExecResult{Stdout: "old-init", ExitCode: 0, DurationMs: 5, DurationUs: 0},
			wantUsWire: false,
		},
		{
			name:       "agent error preserves durations",
			result:     ExecResult{ExitCode: -1, Error: "boom", DurationMs: 1, DurationUs: 1200},
			wantUsWire: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()

			go func() {
				if err := WriteMessage(client, tc.result); err != nil {
					t.Errorf("WriteMessage: %v", err)
				}
			}()

			var got ExecResult
			if err := ReadMessage(server, &got); err != nil {
				t.Fatalf("ReadMessage: %v", err)
			}
			if got != tc.result {
				t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, tc.result)
			}

			// Confirm the omitempty behavior on the wire so old hosts that
			// never read duration_us stay unaffected.
			payload, err := json.Marshal(tc.result)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			gotUsWire := bytes.Contains(payload, []byte(`"duration_us"`))
			if gotUsWire != tc.wantUsWire {
				t.Fatalf("duration_us present = %v, want %v (json %s)", gotUsWire, tc.wantUsWire, payload)
			}
			// duration_ms is always present, even when zero.
			if !bytes.Contains(payload, []byte(`"duration_ms"`)) {
				t.Fatalf("duration_ms missing from wire: %s", payload)
			}
		})
	}
}

func TestReadMessageRejectsOversizeLength(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		// 0xFFFFFFFF length prefix, far over the cap.
		_, _ = client.Write([]byte{0xff, 0xff, 0xff, 0xff})
	}()

	var got ExecResult
	if err := ReadMessage(server, &got); err == nil {
		t.Fatal("expected error for oversize message, got nil")
	}
}

func TestDialGuestHandshake(t *testing.T) {
	dir := t.TempDir()
	uds := filepath.Join(dir, "vsock.sock")

	ln, err := net.Listen("unix", uds)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Fake Firecracker host socket: expect "CONNECT <port>\n", reply "OK ...\n",
	// then echo one framed ExecRequest back as an ExecResult-shaped message.
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		line, err := br.ReadString('\n')
		if err != nil {
			done <- err
			return
		}
		var port uint32
		if _, err := fmt.Sscanf(line, "CONNECT %d\n", &port); err != nil {
			done <- fmt.Errorf("parse CONNECT: %w (line %q)", err, line)
			return
		}
		if port != GuestPort {
			done <- fmt.Errorf("got port %d, want %d", port, GuestPort)
			return
		}
		if _, err := fmt.Fprintf(conn, "OK %d\n", 12345); err != nil {
			done <- err
			return
		}
		// Read the request the client sends, reply with a result.
		var req ExecRequest
		if err := ReadMessage(br, &req); err != nil {
			done <- err
			return
		}
		done <- WriteMessage(conn, ExecResult{Stdout: req.Code, ExitCode: 0})
	}()

	conn, err := DialGuest(uds, GuestPort)
	if err != nil {
		t.Fatalf("DialGuest: %v", err)
	}
	defer conn.Close()

	if err := WriteMessage(conn, ExecRequest{Code: "echo-me"}); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	var res ExecResult
	if err := ReadMessage(conn, &res); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if res.Stdout != "echo-me" {
		t.Fatalf("got stdout %q, want %q", res.Stdout, "echo-me")
	}
	if err := <-done; err != nil {
		t.Fatalf("server side: %v", err)
	}
}
