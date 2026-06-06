package main

import (
	"errors"
	"fmt"
	"io"
	"log"

	"golang.org/x/sys/unix"

	"github.com/hdprajwal/emberd/pkg/proto"
)

// execHandler runs one ExecRequest and returns its result.
type execHandler func(proto.ExecRequest) proto.ExecResult

// serveVsock binds an AF_VSOCK listener on port and serves one ExecRequest /
// ExecResult exchange per accepted connection. It blocks until the listener
// fails.
func serveVsock(port uint32, handle execHandler) error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("create vsock socket: %w", err)
	}

	sa := &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return fmt.Errorf("bind vsock port %d: %w", port, err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		unix.Close(fd)
		return fmt.Errorf("listen vsock: %w", err)
	}
	defer unix.Close(fd)

	log.Printf("emberd-init listening on vsock port %d", port)
	for {
		nfd, _, err := unix.Accept(fd)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return fmt.Errorf("accept vsock: %w", err)
		}
		go serveConn(nfd, handle)
	}
}

func serveConn(fd int, handle execHandler) {
	conn := &vsockConn{fd: fd}
	defer conn.Close()

	var req proto.ExecRequest
	if err := proto.ReadMessage(conn, &req); err != nil {
		// A clean close before any bytes is a host readiness probe (the daemon
		// dials, completes the handshake, and hangs up to confirm we're up), not
		// an error worth logging.
		if !errors.Is(err, io.EOF) {
			log.Printf("read exec request: %v", err)
		}
		return
	}
	res := handle(req)
	if err := proto.WriteMessage(conn, res); err != nil {
		log.Printf("write exec result: %v", err)
	}
}

// vsockConn adapts a raw AF_VSOCK file descriptor to io.Reader/io.Writer.
// Go's net package can't wrap vsock fds (net.FileConn rejects the address
// family), so the framing helpers talk to the fd directly.
type vsockConn struct{ fd int }

func (c *vsockConn) Read(p []byte) (int, error) {
	for {
		n, err := unix.Read(c.fd, p)
		if err == unix.EINTR {
			continue
		}
		if n == 0 && err == nil && len(p) > 0 {
			return 0, io.EOF
		}
		return n, err
	}
}

func (c *vsockConn) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		n, err := unix.Write(c.fd, p[written:])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return written, err
		}
		written += n
	}
	return written, nil
}

func (c *vsockConn) Close() error { return unix.Close(c.fd) }
