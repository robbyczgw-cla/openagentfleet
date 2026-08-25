package compute

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadableDockerOutputDoesNotTruncate500Chars(t *testing.T) {
	body := strings.Repeat("load metadata for docker.io/library/ubuntu:24.04 ", 12)
	if len(body) < 500 {
		t.Fatalf("fixture too short: %d", len(body))
	}
	got := readableDockerOutput(body)
	if !strings.Contains(got, "load metadata for docker.io/library/ubuntu:24.04") {
		t.Fatalf("lost metadata line: %q", got)
	}
	if len(got) < 500 {
		t.Fatalf("truncated a 500-char error: %d", len(got))
	}
}

func TestCompactStillShortensLongStrings(t *testing.T) {
	body := strings.Repeat("x", 500)
	got := compact(body)
	if len(got) != 240 {
		t.Fatalf("compact len = %d, want 240", len(got))
	}
	if readableDockerOutput(body) == got {
		t.Fatal("compact and readableDockerOutput must differ for long logs")
	}
}

func TestClassifyDockerDaemonError(t *testing.T) {
	tests := []struct {
		err  string
		want dockerDaemonErrorKind
	}{
		{"error during connect: failed to connect to npipe:////./pipe/docker_engine", dockerDaemonErrorEngineDown},
		{"Cannot connect to the Docker daemon. Is the docker daemon running?", dockerDaemonErrorEngineDown},
		{"open named pipe //./pipe/docker_engine: The system cannot find the file specified", dockerDaemonErrorEngineDown},
		{"docker-credential-wincred.exe: Eine angegebene Anmeldesitzung ist nicht vorhanden", dockerDaemonErrorWincredSession},
		{"error getting credentials: A specified logon session does not exist", dockerDaemonErrorWincredSession},
		{"permission denied while trying to connect to the docker API", dockerDaemonErrorPermission},
		{"exit status 1: something else", dockerDaemonErrorUnknown},
	}
	for _, test := range tests {
		if got := classifyDockerDaemonError(errors.New(test.err)); got != test.want {
			t.Errorf("classifyDockerDaemonError(%q) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestFormatDockerDaemonUnavailableWindowsDesktop(t *testing.T) {
	detail := formatDockerDaemonUnavailable("windows", errors.New("error during connect: failed to connect to npipe:////./pipe/docker_engine: The system cannot find the file specified"))
	if !strings.Contains(detail, "Open Docker Desktop") {
		t.Fatalf("missing Desktop instruction: %q", detail)
	}
	if !strings.Contains(detail, "wait until the engine is ready") {
		t.Fatalf("missing wait instruction: %q", detail)
	}
	if !strings.Contains(detail, "service in Running is not enough") {
		t.Fatalf("missing service warning: %q", detail)
	}
	if !strings.Contains(detail, "npipe:////./pipe/docker_engine") {
		t.Fatalf("truncated pipe error: %q", detail)
	}
}

func TestFormatDockerDaemonUnavailableWincredSession(t *testing.T) {
	german := formatDockerDaemonUnavailable("windows", errors.New("error getting credentials: docker-credential-wincred.exe: Eine angegebene Anmeldesitzung ist nicht vorhanden. Sie wurde gegebenenfalls bereits beendet."))
	if !strings.Contains(german, "Credential Manager") {
		t.Fatalf("missing Credential Manager: %q", german)
	}
	if !strings.Contains(german, "signed-in user") {
		t.Fatalf("missing signed-in user: %q", german)
	}
	if !strings.Contains(german, "ubuntu:24.04") {
		t.Fatalf("missing public-image note: %q", german)
	}
	if !strings.Contains(german, "Anmeldesitzung") {
		t.Fatalf("truncated German wincred error: %q", german)
	}

	english := formatDockerDaemonUnavailable("linux", errors.New("error getting credentials: docker-credential-wincred.exe: A specified logon session does not exist. It may already have been terminated."))
	if !strings.Contains(english, "wincred") || !strings.Contains(english, "specified logon session") {
		t.Fatalf("english wincred mapper: %q", english)
	}
}

func TestDockerPublicRegistryEnvSkipsWincred(t *testing.T) {
	if dockerPublicRegistryEnv("linux", t.TempDir()) != nil {
		t.Fatal("non-windows env should be empty")
	}
	dir := t.TempDir()
	env := dockerPublicRegistryEnv("windows", dir)
	if len(env) != 1 || !strings.HasPrefix(env[0], "DOCKER_CONFIG=") {
		t.Fatalf("env = %#v", env)
	}
	configDir := strings.TrimPrefix(env[0], "DOCKER_CONFIG=")
	body, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{}" {
		t.Fatalf("config = %q, want {}", body)
	}
	if strings.Contains(string(body), "credsStore") {
		t.Fatal("credsStore must not be set")
	}
}
