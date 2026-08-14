package compute

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const remoteWorkerMaxBodyBytes = 128 << 10

// RemoteWorker exposes the already token-gated Agent Computer contract from a
// host that owns Docker/Colima. It is intentionally separate from botd: the
// worker has no harness credentials, chat state, or Docker socket API.
type RemoteWorker struct {
	Docker *Docker
	Token  string
}

func NewRemoteWorker(docker *Docker, token string) (*RemoteWorker, error) {
	if docker == nil {
		return nil, errors.New("remote computer worker requires Docker")
	}
	token = strings.TrimSpace(token)
	if len(token) < 32 {
		return nil, errors.New("remote computer worker token must contain at least 32 characters")
	}
	return &RemoteWorker{Docker: docker, Token: token}, nil
}

func (w *RemoteWorker) Handler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !w.authorized(request) {
			workerError(response, http.StatusUnauthorized, "unauthorized")
			return
		}
		if request.Method == http.MethodOptions {
			response.WriteHeader(http.StatusNoContent)
			return
		}

		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/status":
			workerJSON(response, http.StatusOK, w.Docker.Status(request.Context()))
		case request.Method == http.MethodPost && request.URL.Path == "/ensure":
			status, err := w.Docker.Ensure(request.Context())
			if err != nil {
				workerError(response, http.StatusBadGateway, "remote Agent Computer could not start")
				return
			}
			workerJSON(response, http.StatusOK, status)
		case request.Method == http.MethodPost && request.URL.Path == "/stop":
			if err := w.Docker.Stop(request.Context()); err != nil {
				workerError(response, http.StatusBadGateway, "remote Agent Computer could not stop")
				return
			}
			response.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodGet && request.URL.Path == "/health":
			view, err := w.Docker.ViewStatus(request.Context())
			if err != nil {
				workerError(response, http.StatusBadGateway, "remote Agent Computer view is unavailable")
				return
			}
			workerJSON(response, http.StatusOK, view)
		case request.Method == http.MethodGet && request.URL.Path == "/frame":
			w.writeFrame(response, request, false)
		case request.Method == http.MethodGet && request.URL.Path == "/desktop-frame":
			w.writeFrame(response, request, true)
		case request.Method == http.MethodGet && request.URL.Path == "/target":
			binding, err := w.Docker.TargetBinding(request.Context(), request.URL.Query().Get("surface"))
			if err != nil {
				workerError(response, http.StatusBadGateway, "remote Agent Computer target is unavailable")
				return
			}
			workerJSON(response, http.StatusOK, binding)
		case request.Method == http.MethodPost && request.URL.Path == "/action":
			w.browserAction(response, request, false)
		case request.Method == http.MethodPost && request.URL.Path == "/desktop/action":
			w.browserAction(response, request, true)
		default:
			workerError(response, http.StatusNotFound, "not found")
		}
	})
}

func (w *RemoteWorker) authorized(request *http.Request) bool {
	expected := "Bearer " + w.Token
	received := request.Header.Get("Authorization")
	if len(received) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(received), []byte(expected)) == 1
}

func (w *RemoteWorker) writeFrame(response http.ResponseWriter, request *http.Request, desktop bool) {
	var (
		frame []byte
		err   error
	)
	if desktop {
		frame, err = w.Docker.DesktopFrame(request.Context())
	} else {
		frame, err = w.Docker.Frame(request.Context())
	}
	if err != nil {
		workerError(response, http.StatusBadGateway, err.Error())
		return
	}
	response.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	response.Header().Set("Content-Type", "image/png")
	response.Header().Set("Content-Length", fmt.Sprintf("%d", len(frame)))
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(frame)
}

func (w *RemoteWorker) browserAction(response http.ResponseWriter, request *http.Request, desktop bool) {
	body, err := io.ReadAll(io.LimitReader(request.Body, remoteWorkerMaxBodyBytes+1))
	if err != nil {
		workerError(response, http.StatusBadRequest, "read action body")
		return
	}
	if len(body) > remoteWorkerMaxBodyBytes {
		workerError(response, http.StatusRequestEntityTooLarge, "action body is too large")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var action BrowserAction
	if err := decoder.Decode(&action); err != nil {
		workerError(response, http.StatusBadRequest, "invalid action")
		return
	}
	var view ViewStatus
	if desktop {
		view, err = w.Docker.DesktopAction(request.Context(), action)
	} else {
		view, err = w.Docker.Action(request.Context(), action)
	}
	if err != nil {
		workerError(response, http.StatusConflict, err.Error())
		return
	}
	workerJSON(response, http.StatusOK, view)
}

func workerJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func workerError(response http.ResponseWriter, status int, message string) {
	workerJSON(response, status, map[string]string{"error": message})
}
