package secrethandoff

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeSocketSubmitsBoundedBinarySecretAndUsesInjectedListener(t *testing.T) {
	manager, err := New(Config{TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	defer manager.Close()
	request, err := manager.Create(CreateRequest{
		RunID:          "run-native",
		ConversationID: "conversation-native",
		Surface:        "computer:desktop-1",
		Purpose:        PurposePassword,
	})
	if err != nil {
		t.Fatalf("Create request: %v", err)
	}

	path := nativeTestSocketPath(t)
	var listenCalls atomic.Int32
	server, err := NewNativeSocketServer(NativeSocketConfig{
		Path:    path,
		Manager: manager,
		Listen: func(network, address string) (net.Listener, error) {
			listenCalls.Add(1)
			if network != "unix" || address != path {
				t.Fatalf("listen = %q, %q", network, address)
			}
			return net.Listen(network, address)
		},
	})
	if err != nil {
		t.Fatalf("NewNativeSocketServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if listenCalls.Load() != 1 {
		t.Fatalf("listen calls = %d, want 1", listenCalls.Load())
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat socket: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %#o, want 0600", got)
	}

	status, message := submitNativeTestFrame(t, path, request.ID, []byte("socket-only-secret"))
	if status != 0 || message != "accepted" {
		t.Fatalf("response = %d, %q", status, message)
	}
	claimed, err := manager.Claim(claimRequest(request))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if string(claimed) != "socket-only-secret" {
		t.Fatal("Claim returned unexpected secret")
	}
	Wipe(claimed)
}

func TestNativeSocketRejectsOversizedFrameWithoutAllocatingPayloadAndRecovers(t *testing.T) {
	manager, err := New(Config{TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	defer manager.Close()
	request, err := manager.Create(CreateRequest{
		RunID:          "run-native",
		ConversationID: "conversation-native",
		Surface:        "browser:tab-1",
		Purpose:        PurposeTwoFactorCode,
	})
	if err != nil {
		t.Fatalf("Create request: %v", err)
	}
	path := nativeTestSocketPath(t)
	server, err := NewNativeSocketServer(NativeSocketConfig{Path: path, Manager: manager})
	if err != nil {
		t.Fatalf("NewNativeSocketServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	connection, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	header := nativeTestHeader(len(request.ID), MaxSecret+1)
	if _, err := connection.Write(header); err != nil {
		t.Fatalf("write oversized header: %v", err)
	}
	status, message := readNativeTestResponse(t, connection)
	_ = connection.Close()
	if status != 1 || message != "invalid protocol frame" {
		t.Fatalf("oversized response = %d, %q", status, message)
	}

	status, message = submitNativeTestFrame(t, path, request.ID, []byte("123456"))
	if status != 0 || message != "accepted" {
		t.Fatalf("recovery response = %d, %q", status, message)
	}
}

func TestNativeSocketTimeoutAndCloseLifecycle(t *testing.T) {
	manager, err := New(Config{TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	defer manager.Close()
	path := nativeTestSocketPath(t)
	server, err := NewNativeSocketServer(NativeSocketConfig{
		Path:      path,
		Manager:   manager,
		IOTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewNativeSocketServer: %v", err)
	}

	connection, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 1)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("idle connection remained open past server timeout")
	}
	_ = connection.Close()

	if err := server.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after Close: %v", err)
	}
	if _, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
		t.Fatal("dial succeeded after Close")
	}
	if !errors.Is(server.Err(), ErrNativeSocketClosed) {
		t.Fatalf("server Err = %v, want ErrNativeSocketClosed", server.Err())
	}
}

func TestNativeSocketRequiresPrivateParentAndDoesNotReplaceExistingPath(t *testing.T) {
	manager, err := New(Config{TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	defer manager.Close()

	unsafeParent := filepath.Dir(nativeTestSocketPath(t))
	if err := os.Chmod(unsafeParent, 0o755); err != nil {
		t.Fatalf("Chmod parent: %v", err)
	}
	if _, err := NewNativeSocketServer(NativeSocketConfig{
		Path:    filepath.Join(unsafeParent, "handoff.sock"),
		Manager: manager,
	}); !errors.Is(err, ErrNativeSocketPermission) {
		t.Fatalf("unsafe parent error = %v", err)
	}

	privateParent := filepath.Dir(nativeTestSocketPath(t))
	existingPath := filepath.Join(privateParent, "handoff.sock")
	if err := os.WriteFile(existingPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewNativeSocketServer(NativeSocketConfig{
		Path:    existingPath,
		Manager: manager,
	}); !errors.Is(err, ErrNativeSocketConfig) {
		t.Fatalf("existing path error = %v", err)
	}
	contents, err := os.ReadFile(existingPath)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("existing path changed: %q, %v", contents, err)
	}
}

func TestNativeSocketReclaimsOnlyAProvenStaleSocket(t *testing.T) {
	manager, err := New(Config{TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	defer manager.Close()
	path := nativeTestSocketPath(t)
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen stale socket: %v", err)
	}
	staleUnix := stale.(*net.UnixListener)
	staleUnix.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("stale socket missing: %v", err)
	}

	server, err := NewNativeSocketServer(NativeSocketConfig{Path: path, Manager: manager})
	if err != nil {
		t.Fatalf("reclaim stale socket: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if _, err := net.DialTimeout("unix", path, time.Second); err != nil {
		t.Fatalf("reclaimed socket is not live: %v", err)
	}
}

func TestNativeSocketDoesNotReplaceLiveSocket(t *testing.T) {
	manager, err := New(Config{TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	defer manager.Close()
	path := nativeTestSocketPath(t)
	live, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen live socket: %v", err)
	}
	liveUnix := live.(*net.UnixListener)
	liveUnix.SetUnlinkOnClose(false)
	defer func() {
		_ = live.Close()
		_ = os.Remove(path)
	}()
	original, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat live socket: %v", err)
	}

	if _, err := NewNativeSocketServer(NativeSocketConfig{Path: path, Manager: manager}); !errors.Is(err, ErrNativeSocketConfig) {
		t.Fatalf("live socket error = %v", err)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(original, current) {
		t.Fatalf("live socket changed: %v", err)
	}
}

func TestNativeSocketCleanupDoesNotRemoveReplacementSocket(t *testing.T) {
	path := nativeTestSocketPath(t)
	original, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen original: %v", err)
	}
	originalUnix := original.(*net.UnixListener)
	originalUnix.SetUnlinkOnClose(false)
	defer original.Close()
	originalInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat original: %v", err)
	}
	movedPath := path + ".moved"
	if err := os.Rename(path, movedPath); err != nil {
		t.Fatalf("rename original: %v", err)
	}
	defer os.Remove(movedPath)

	replacement, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen replacement: %v", err)
	}
	replacementUnix := replacement.(*net.UnixListener)
	replacementUnix.SetUnlinkOnClose(false)
	defer func() {
		_ = replacement.Close()
		_ = os.Remove(path)
	}()

	removeSocketIfSame(path, originalInfo)
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("replacement socket was removed: %v", err)
	}
}

func TestNativeSocketDiagnosticsDoNotContainRequestOrSecret(t *testing.T) {
	manager, err := New(Config{TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	defer manager.Close()
	path := nativeTestSocketPath(t)
	server, err := NewNativeSocketServer(NativeSocketConfig{Path: path, Manager: manager})
	if err != nil {
		t.Fatalf("NewNativeSocketServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	id := "handoff_00000000000000000000000000000000"
	secret := "diagnostic-sentinel-value"
	status, message := submitNativeTestFrame(t, path, id, []byte(secret))
	if status != 2 {
		t.Fatalf("status = %d, want rejection", status)
	}
	if strings.Contains(message, id) || strings.Contains(message, secret) {
		t.Fatalf("diagnostic leaked request data: %q", message)
	}
}

func TestNativeSocketOnAcceptedCanDeliverWithoutReceivingSecretArgument(t *testing.T) {
	manager, err := New(Config{TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	defer manager.Close()
	request, err := manager.Create(CreateRequest{
		RunID:          "run-native",
		ConversationID: "conversation-native",
		Surface:        "computer:desktop-1",
		Purpose:        PurposePassword,
	})
	if err != nil {
		t.Fatalf("Create request: %v", err)
	}

	delivered := make(chan []byte, 1)
	path := nativeTestSocketPath(t)
	server, err := NewNativeSocketServer(NativeSocketConfig{
		Path:    path,
		Manager: manager,
		OnAccepted: func(ctx context.Context, handoffID string) error {
			if handoffID != request.ID {
				return errors.New("unexpected handoff")
			}
			secret, err := manager.Claim(claimRequest(request))
			if err != nil {
				return err
			}
			copyForAssertion := append([]byte(nil), secret...)
			Wipe(secret)
			select {
			case delivered <- copyForAssertion:
				return nil
			case <-ctx.Done():
				Wipe(copyForAssertion)
				return ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatalf("NewNativeSocketServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	status, message := submitNativeTestFrame(t, path, request.ID, []byte("delivered-on-accept"))
	if status != 0 || message != "accepted" {
		t.Fatalf("response = %d, %q", status, message)
	}
	secret := <-delivered
	if string(secret) != "delivered-on-accept" {
		t.Fatal("callback delivery received unexpected secret")
	}
	Wipe(secret)
}

func TestNativeSocketOnAcceptedFailureIsGeneric(t *testing.T) {
	manager, err := New(Config{TTL: 30 * time.Second})
	if err != nil {
		t.Fatalf("New manager: %v", err)
	}
	defer manager.Close()
	request, err := manager.Create(CreateRequest{
		RunID:          "run-native",
		ConversationID: "conversation-native",
		Surface:        "browser:tab-1",
		Purpose:        PurposePassword,
	})
	if err != nil {
		t.Fatalf("Create request: %v", err)
	}
	secretText := "callback-secret-sentinel"
	callbackError := "callback failed for " + request.ID + " with " + secretText
	path := nativeTestSocketPath(t)
	server, err := NewNativeSocketServer(NativeSocketConfig{
		Path:    path,
		Manager: manager,
		OnAccepted: func(context.Context, string) error {
			return errors.New(callbackError)
		},
	})
	if err != nil {
		t.Fatalf("NewNativeSocketServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	status, message := submitNativeTestFrame(t, path, request.ID, []byte(secretText))
	if status != 4 || message != "delivery failed" {
		t.Fatalf("callback failure response = %d, %q", status, message)
	}
	if strings.Contains(message, request.ID) || strings.Contains(message, secretText) || strings.Contains(message, callbackError) {
		t.Fatalf("callback failure leaked details: %q", message)
	}
	state, err := manager.Get(request.ID)
	if err != nil {
		t.Fatalf("Get after callback failure: %v", err)
	}
	if state.Ready || state.Status != StatusCancelled {
		t.Fatalf("failed delivery did not clear submitted value: %+v", state)
	}
}

func submitNativeTestFrame(t *testing.T, path, id string, secret []byte) (byte, string) {
	t.Helper()
	connection, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	header := nativeTestHeader(len(id), len(secret))
	if _, err := connection.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := io.WriteString(connection, id); err != nil {
		t.Fatalf("write id: %v", err)
	}
	if _, err := connection.Write(secret); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	return readNativeTestResponse(t, connection)
}

func nativeTestHeader(idLength, secretLength int) []byte {
	header := make([]byte, nativeSocketHeaderSize)
	copy(header[:4], nativeSocketMagic[:])
	header[4] = nativeSocketProtocolVersion
	binary.BigEndian.PutUint16(header[6:8], uint16(idLength))
	binary.BigEndian.PutUint32(header[8:12], uint32(secretLength))
	return header
}

func readNativeTestResponse(t *testing.T, reader io.Reader) (byte, string) {
	t.Helper()
	header := make([]byte, nativeSocketResponseHeaderSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		t.Fatalf("read response header: %v", err)
	}
	if string(header[:4]) != string(nativeSocketMagic[:]) || header[4] != nativeSocketProtocolVersion {
		t.Fatalf("invalid response header: %v", header)
	}
	message := make([]byte, int(binary.BigEndian.Uint16(header[6:8])))
	if _, err := io.ReadFull(reader, message); err != nil {
		t.Fatalf("read response message: %v", err)
	}
	return header[5], string(message)
}

func nativeTestSocketPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native secret-handoff socket is unix-only")
	}
	directory, err := os.MkdirTemp("/tmp", "ofb-handoff-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		t.Fatalf("Chmod temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, "handoff.sock")
}
