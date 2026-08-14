package secrethandoff

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	defaultNativeSocketTimeout          = 5 * time.Second
	defaultNativeSocketConcurrency      = 8
	maxNativeSocketConcurrency          = 64
	nativeSocketProtocolVersion    byte = 1
	nativeSocketHeaderSize              = 12
	nativeSocketResponseHeaderSize      = 8
	maxNativeSocketResponseBytes        = 128
)

var (
	nativeSocketMagic         = [4]byte{'O', 'F', 'B', 'H'}
	ErrNativeSocketConfig     = errors.New("secret handoff native socket: invalid configuration")
	ErrNativeSocketProtocol   = errors.New("secret handoff native socket: invalid protocol frame")
	ErrNativeSocketClosed     = errors.New("secret handoff native socket: closed")
	ErrNativeSocketPermission = errors.New("secret handoff native socket: unsafe socket permissions")
)

// NativeSocketListenFunc makes socket creation injectable while retaining the
// production net.Listen("unix", path) behavior. Implementations must create a
// Unix-domain socket at the supplied path.
type NativeSocketListenFunc func(network, path string) (net.Listener, error)

// NativeSocketAcceptedFunc is invoked after Manager.Submit succeeds. It is
// deliberately given only the handoff ID, never the secret. A callback may
// claim and deliver the submitted value, so its failure does not imply that
// the value remains available in Manager.
type NativeSocketAcceptedFunc func(ctx context.Context, handoffID string) error

// NativeSocketConfig configures a bounded, local-only transport into
// Manager.Submit. Path must be absolute and its existing parent directory must
// not grant group or other permissions. The listener does not own Manager and
// does not close it.
type NativeSocketConfig struct {
	Path          string
	Manager       *Manager
	Listen        NativeSocketListenFunc
	OnAccepted    NativeSocketAcceptedFunc
	IOTimeout     time.Duration
	MaxConcurrent int
}

// NativeSocketServer accepts one binary request per Unix socket connection.
//
// Request wire format (network byte order):
//
//	4 bytes magic "OFBH"
//	1 byte  protocol version (1)
//	1 byte  reserved (must be zero)
//	2 bytes handoff ID length (1..256)
//	4 bytes secret length (1..MaxSecret)
//	N bytes handoff ID
//	M bytes secret
//
// Responses use the same magic, followed by version, one status byte, a
// uint16 message length, and a bounded UTF-8 diagnostic. Status zero means the
// manager accepted the secret. Neither request values nor secret bytes are
// included in diagnostics.
type NativeSocketServer struct {
	manager    *Manager
	listener   net.Listener
	path       string
	socketInfo os.FileInfo
	timeout    time.Duration
	semaphore  chan struct{}
	onAccepted NativeSocketAcceptedFunc
	context    context.Context
	cancel     context.CancelFunc

	serveDone chan struct{}
	workers   sync.WaitGroup
	closeOnce sync.Once
	closeErr  error

	errMu    sync.Mutex
	serveErr error
}

// NewNativeSocketServer binds the configured socket, restricts its mode to
// 0600, and starts accepting connections. It never replaces a pre-existing
// regular file or a live socket. A proven stale Unix socket is reclaimed so a
// clean app restart can recover after a forced process termination.
func NewNativeSocketServer(config NativeSocketConfig) (*NativeSocketServer, error) {
	if config.Manager == nil || !filepath.IsAbs(config.Path) {
		return nil, ErrNativeSocketConfig
	}
	parent, err := os.Stat(filepath.Dir(config.Path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0o077 != 0 {
		return nil, ErrNativeSocketPermission
	}
	if existing, err := os.Lstat(config.Path); err == nil {
		if existing.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%w: path already exists", ErrNativeSocketConfig)
		}
		if err := reclaimStaleSocket(config.Path, existing); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: inspect socket path: %v", ErrNativeSocketConfig, err)
	}

	timeout := config.IOTimeout
	if timeout == 0 {
		timeout = defaultNativeSocketTimeout
	}
	if timeout < 100*time.Millisecond || timeout > 30*time.Second {
		return nil, ErrNativeSocketConfig
	}
	maxConcurrent := config.MaxConcurrent
	if maxConcurrent == 0 {
		maxConcurrent = defaultNativeSocketConcurrency
	}
	if maxConcurrent < 1 || maxConcurrent > maxNativeSocketConcurrency {
		return nil, ErrNativeSocketConfig
	}
	listen := config.Listen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("unix", config.Path)
	if err != nil {
		return nil, fmt.Errorf("secret handoff native socket: listen: %w", err)
	}
	var socketInfo os.FileInfo
	cleanup := func() {
		_ = listener.Close()
		removeSocketIfSame(config.Path, socketInfo)
	}
	if listener.Addr() == nil || listener.Addr().Network() != "unix" {
		cleanup()
		return nil, fmt.Errorf("%w: listener is not unix-domain", ErrNativeSocketConfig)
	}
	socketInfo, err = os.Lstat(config.Path)
	if err != nil || socketInfo.Mode()&os.ModeSocket == 0 {
		cleanup()
		return nil, ErrNativeSocketPermission
	}
	if err := os.Chmod(config.Path, 0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: chmod: %v", ErrNativeSocketPermission, err)
	}
	info, err := os.Lstat(config.Path)
	if err != nil || !os.SameFile(socketInfo, info) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		cleanup()
		return nil, ErrNativeSocketPermission
	}
	socketInfo = info

	serverContext, cancel := context.WithCancel(context.Background())
	server := &NativeSocketServer{
		manager:    config.Manager,
		listener:   listener,
		path:       config.Path,
		socketInfo: socketInfo,
		timeout:    timeout,
		semaphore:  make(chan struct{}, maxConcurrent),
		onAccepted: config.OnAccepted,
		context:    serverContext,
		cancel:     cancel,
		serveDone:  make(chan struct{}),
	}
	go server.serve()
	return server, nil
}

func reclaimStaleSocket(path string, original os.FileInfo) error {
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return fmt.Errorf("%w: socket is already active", ErrNativeSocketConfig)
	}
	// Only connection-refused / vanished entries are demonstrably stale. A
	// timeout or any other connection error may be a live socket under load and
	// must remain untouched.
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("%w: existing socket cannot be safely reclaimed", ErrNativeSocketConfig)
	}
	removeSocketIfSame(path, original)
	if current, statErr := os.Lstat(path); statErr == nil {
		if current.Mode()&os.ModeSocket != 0 && os.SameFile(original, current) {
			return fmt.Errorf("%w: stale socket could not be removed", ErrNativeSocketConfig)
		}
		return fmt.Errorf("%w: socket path changed while reclaiming", ErrNativeSocketConfig)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect reclaimed socket path", ErrNativeSocketConfig)
	}
	return nil
}

// Path returns the configured Unix socket path.
func (s *NativeSocketServer) Path() string { return s.path }

// Err reports an unexpected terminal accept error. A normal Close reports
// ErrNativeSocketClosed.
func (s *NativeSocketServer) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.serveErr
}

// Close stops accepting, waits for active bounded handlers, and removes the
// socket path if it is still a Unix socket. Close is idempotent.
func (s *NativeSocketServer) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.listener.Close()
		s.cancel()
		<-s.serveDone
		s.workers.Wait()
		removeSocketIfSame(s.path, s.socketInfo)
		if errors.Is(s.closeErr, net.ErrClosed) {
			s.closeErr = nil
		}
	})
	return s.closeErr
}

func (s *NativeSocketServer) serve() {
	defer close(s.serveDone)
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				s.setServeErr(ErrNativeSocketClosed)
				return
			}
			s.setServeErr(fmt.Errorf("secret handoff native socket: accept: %w", err))
			return
		}
		select {
		case s.semaphore <- struct{}{}:
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer func() { <-s.semaphore }()
				s.handle(connection)
			}()
		default:
			_ = connection.SetDeadline(time.Now().Add(s.timeout))
			_ = writeNativeSocketResponse(connection, 3, "server busy")
			_ = connection.Close()
		}
	}
}

func (s *NativeSocketServer) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(s.timeout))

	id, secret, err := readNativeSocketRequest(connection)
	if secret != nil {
		defer Wipe(secret)
	}
	if id != nil {
		defer Wipe(id)
	}
	if err != nil {
		_ = writeNativeSocketResponse(connection, 1, "invalid protocol frame")
		return
	}

	if err := s.manager.Submit(string(id), secret); err != nil {
		_ = writeNativeSocketResponse(connection, 2, nativeSubmitDiagnostic(err))
		return
	}
	if s.onAccepted != nil {
		_ = connection.SetDeadline(time.Now().Add(s.timeout))
		callbackContext, cancel := context.WithTimeout(s.context, s.timeout)
		err := s.onAccepted(callbackContext, string(id))
		cancel()
		if err != nil {
			// A failed delivery must not leave a secret resident with no safe
			// retry trigger. If the callback already claimed it, Cancel simply
			// fails closed and the value is already wiped by Claim.
			_, _ = s.manager.Cancel(string(id))
			_ = writeNativeSocketResponse(connection, 4, "delivery failed")
			return
		}
	}
	_ = writeNativeSocketResponse(connection, 0, "accepted")
}

func readNativeSocketRequest(reader io.Reader) ([]byte, []byte, error) {
	header := make([]byte, nativeSocketHeaderSize)
	defer Wipe(header)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, nil, ErrNativeSocketProtocol
	}
	if !bytes.Equal(header[:4], nativeSocketMagic[:]) ||
		header[4] != nativeSocketProtocolVersion || header[5] != 0 {
		return nil, nil, ErrNativeSocketProtocol
	}
	idLength := int(binary.BigEndian.Uint16(header[6:8]))
	secretLength := int(binary.BigEndian.Uint32(header[8:12]))
	if idLength < 1 || idLength > 256 || secretLength < 1 || secretLength > MaxSecret {
		return nil, nil, ErrNativeSocketProtocol
	}

	id := make([]byte, idLength)
	if _, err := io.ReadFull(reader, id); err != nil {
		Wipe(id)
		return nil, nil, ErrNativeSocketProtocol
	}
	secret := make([]byte, secretLength)
	if _, err := io.ReadFull(reader, secret); err != nil {
		Wipe(id)
		Wipe(secret)
		return nil, nil, ErrNativeSocketProtocol
	}
	return id, secret, nil
}

func writeNativeSocketResponse(writer io.Writer, status byte, message string) error {
	if len(message) > maxNativeSocketResponseBytes {
		message = message[:maxNativeSocketResponseBytes]
	}
	header := make([]byte, nativeSocketResponseHeaderSize)
	copy(header[:4], nativeSocketMagic[:])
	header[4] = nativeSocketProtocolVersion
	header[5] = status
	binary.BigEndian.PutUint16(header[6:8], uint16(len(message)))
	if _, err := writer.Write(header); err != nil {
		return err
	}
	_, err := io.WriteString(writer, message)
	return err
}

func nativeSubmitDiagnostic(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "handoff not found"
	case errors.Is(err, ErrNotPending):
		return "handoff not pending"
	case errors.Is(err, ErrAlreadySubmitted):
		return "handoff already submitted"
	case errors.Is(err, ErrInvalidSecret):
		return "invalid secret"
	case errors.Is(err, ErrClosed):
		return "handoff manager closed"
	default:
		return "handoff rejected"
	}
}

func (s *NativeSocketServer) setServeErr(err error) {
	s.errMu.Lock()
	s.serveErr = err
	s.errMu.Unlock()
}

func removeSocketIfSame(path string, original os.FileInfo) {
	if original == nil {
		return
	}
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSocket != 0 && os.SameFile(original, info) {
		_ = os.Remove(path)
	}
}
