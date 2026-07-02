// Package proto defines the vsock control protocol spoken between the emberd
// host daemon and the emberd-init guest agent.
//
// Wire format: each message is a 4-byte big-endian unsigned length prefix
// followed by that many bytes of JSON. One ExecRequest from host to guest, one
// ExecResult back, per connection.
package proto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// GuestPort is the vsock port emberd-init listens on inside the guest.
const GuestPort uint32 = 1024

// maxMessageBytes caps a single message to guard against a corrupt or hostile
// length prefix forcing a huge allocation.
const maxMessageBytes = 64 << 20 // 64 MiB

// ExecRequest is sent host -> guest to run code in the sandbox.
type ExecRequest struct {
	Code      string `json:"code"`
	Stdin     string `json:"stdin,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	// HostTimeUnixNano is the host wall clock at send time, used by the guest
	// to resync its own clock after a snapshot restore (which resumes with the
	// snapshot's stale clock). A zero value means "don't touch the clock" —
	// older hosts omit it and older guests ignore it, so it is backward and
	// forward compatible.
	HostTimeUnixNano int64 `json:"host_time_unix_nano,omitempty"`
}

// ExecResult is sent guest -> host with the outcome of an ExecRequest.
// Error is non-empty only when the guest agent failed to run the request at
// all (e.g. spawning the interpreter); a non-zero ExitCode from the user's code
// is a normal result, not an Error.
//
// DurationMs and DurationUs both report the guest-measured wall time of the
// exec, in whole milliseconds and whole microseconds respectively, from a
// single clock reading so the two never disagree. DurationMs is kept for
// compatibility with older hosts; DurationUs (omitted when zero) gives the
// finer resolution newer hosts prefer. The additive DurationUs field means old
// init + new host and new init + old host stay wire-compatible.
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int    `json:"duration_ms"`
	DurationUs int64  `json:"duration_us,omitempty"`
	Error      string `json:"error,omitempty"`
}

// WriteMessage frames v as length-prefixed JSON and writes it to w.
func WriteMessage(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	if len(payload) > maxMessageBytes {
		return fmt.Errorf("message too large: %d bytes", len(payload))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("write length prefix: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}

// ReadMessage reads one length-prefixed JSON message from r into v.
func ReadMessage(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return fmt.Errorf("read length prefix: %w", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxMessageBytes {
		return fmt.Errorf("message too large: %d bytes", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	if err := json.Unmarshal(payload, v); err != nil {
		return fmt.Errorf("unmarshal message: %w", err)
	}
	return nil
}

// DialGuest connects to a guest vsock port through a Firecracker hybrid-vsock
// host Unix socket. Firecracker's host-initiated connection protocol is: open
// the Unix socket, send "CONNECT <port>\n", then read an "OK <hostport>\n"
// acknowledgement line; subsequent bytes are the raw guest stream.
func DialGuest(udsPath string, port uint32) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", udsPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial vsock host socket %s: %w", udsPath, err)
	}
	// Bound the handshake; cleared before returning so callers set their own.
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}
	// Read the ack a byte at a time so we never buffer past the newline into
	// the guest's data stream.
	line, err := readLine(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT ack: %w", err)
	}
	if len(line) < 2 || line[:2] != "OK" {
		conn.Close()
		return nil, fmt.Errorf("unexpected CONNECT ack: %q", line)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// readLine reads bytes up to and including the first '\n' and returns the line
// without the trailing newline.
func readLine(r io.Reader) (string, error) {
	var buf []byte
	var b [1]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", err
		}
		if b[0] == '\n' {
			return string(buf), nil
		}
		buf = append(buf, b[0])
		if len(buf) > 256 {
			return "", fmt.Errorf("ack line too long")
		}
	}
}
