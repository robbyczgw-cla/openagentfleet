package compute

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxComputerSnapshots = 8
	maxSnapshotNameBytes = 80
	snapshotImagePrefix  = "openagentfleet-agent-computer-snap:"
	snapshotVolumePrefix = "openagentfleet-snap-profile-"
)

// SemanticElement is one interactive control from the guest accessibility or
// DOM walk. Agents should act by Ref first and fall back to coordinates.
type SemanticElement struct {
	Ref    string `json:"ref"`
	Role   string `json:"role,omitempty"`
	Name   string `json:"name,omitempty"`
	Tag    string `json:"tag,omitempty"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// SemanticSnapshot is the compact AX-first observation the Agent Computer
// exposes. It never includes typed secrets or screenshot bytes.
type SemanticSnapshot struct {
	Surface  string             `json:"surface"`
	URL      string             `json:"url,omitempty"`
	Title    string             `json:"title,omitempty"`
	Ladder   []string           `json:"ladder"`
	Elements []SemanticElement  `json:"elements"`
	Detail   string             `json:"detail,omitempty"`
}

// ComputerSnapshot is a named checkpoint of the isolated computer image and
// Chromium profile. It is not a VM clone and does not capture host files.
type ComputerSnapshot struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"created_at"`
	Image         string    `json:"image"`
	ProfileVolume string    `json:"profile_volume,omitempty"`
	Active        bool      `json:"active,omitempty"`
}

type snapshotCatalog struct {
	Snapshots   []ComputerSnapshot `json:"snapshots"`
	ActiveImage string             `json:"active_image,omitempty"`
	ActiveID    string             `json:"active_id,omitempty"`
}

func (d *Docker) SemanticSnapshot(ctx context.Context, surface string) (SemanticSnapshot, error) {
	surface = strings.TrimSpace(surface)
	if surface != "browser" && surface != "desktop" {
		return SemanticSnapshot{}, errors.New("snapshot surface must be browser or desktop")
	}
	if !d.AllowExecution {
		return SemanticSnapshot{}, ErrExecutionDisabled
	}
	response, err := d.viewRequest(ctx, http.MethodGet, "/snapshot?surface="+urlQueryEscape(surface), nil)
	if err != nil {
		return SemanticSnapshot{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return SemanticSnapshot{}, err
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		if failure.Error != "" {
			return SemanticSnapshot{}, errors.New(failure.Error)
		}
		return SemanticSnapshot{}, fmt.Errorf("computer snapshot returned HTTP %d", response.StatusCode)
	}
	var snapshot SemanticSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return SemanticSnapshot{}, fmt.Errorf("decode computer snapshot: %w", err)
	}
	if snapshot.Surface == "" {
		snapshot.Surface = surface
	}
	if len(snapshot.Ladder) == 0 {
		snapshot.Ladder = []string{"element", "pixel"}
	}
	return snapshot, nil
}

func (d *Docker) ListSnapshots() ([]ComputerSnapshot, error) {
	catalog, err := d.loadSnapshotCatalog()
	if err != nil {
		return nil, err
	}
	items := make([]ComputerSnapshot, 0, len(catalog.Snapshots))
	for _, item := range catalog.Snapshots {
		item.Active = item.ID == catalog.ActiveID || item.Image == catalog.ActiveImage
		items = append(items, item)
	}
	return items, nil
}

func (d *Docker) CreateSnapshot(ctx context.Context, name string) (ComputerSnapshot, error) {
	if !d.AllowExecution {
		return ComputerSnapshot{}, ErrExecutionDisabled
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxSnapshotNameBytes {
		return ComputerSnapshot{}, errors.New("snapshot name is required and must stay under 80 characters")
	}
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.remoteEnabled() {
		return d.remoteCreateSnapshot(ctx, name)
	}
	status := d.Status(ctx)
	if !status.Running {
		return ComputerSnapshot{}, errors.New("start the Agent Computer before saving a snapshot")
	}
	catalog, err := d.loadSnapshotCatalog()
	if err != nil {
		return ComputerSnapshot{}, err
	}
	if len(catalog.Snapshots) >= maxComputerSnapshots {
		return ComputerSnapshot{}, fmt.Errorf("at most %d computer snapshots are kept; delete one first", maxComputerSnapshots)
	}
	id, err := newSnapshotID()
	if err != nil {
		return ComputerSnapshot{}, err
	}
	image := snapshotImagePrefix + id
	profileVolume := snapshotVolumePrefix + id
	_ = d.run(ctx, "unpause", d.ContainerName)
	if err := d.run(ctx, "pause", d.ContainerName); err != nil {
		return ComputerSnapshot{}, fmt.Errorf("pause Agent Computer for snapshot: %w", err)
	}
	paused := true
	defer func() {
		if paused {
			_ = d.run(context.Background(), "unpause", d.ContainerName)
		}
	}()
	if _, err := d.runOutputWithTimeout(ctx, 3*time.Minute, "commit", d.ContainerName, image); err != nil {
		return ComputerSnapshot{}, fmt.Errorf("commit Agent Computer snapshot: %w", err)
	}
	if volume := strings.TrimSpace(d.BrowserProfileVolume); volume != "" {
		if err := d.cloneVolume(ctx, volume, profileVolume); err != nil {
			_ = d.run(ctx, "rmi", "--force", image)
			return ComputerSnapshot{}, fmt.Errorf("copy browser profile into snapshot: %w", err)
		}
	} else {
		profileVolume = ""
	}
	if err := d.run(ctx, "unpause", d.ContainerName); err != nil {
		return ComputerSnapshot{}, fmt.Errorf("resume Agent Computer after snapshot: %w", err)
	}
	paused = false
	item := ComputerSnapshot{
		ID:            id,
		Name:          name,
		CreatedAt:     time.Now().UTC(),
		Image:         image,
		ProfileVolume: profileVolume,
	}
	catalog.Snapshots = append([]ComputerSnapshot{item}, catalog.Snapshots...)
	if err := d.saveSnapshotCatalog(catalog); err != nil {
		return ComputerSnapshot{}, err
	}
	return item, nil
}

func (d *Docker) RestoreSnapshot(ctx context.Context, id string) (Status, error) {
	if !d.AllowExecution {
		return d.baseStatus(), ErrExecutionDisabled
	}
	id = strings.TrimSpace(id)
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.remoteEnabled() {
		return d.remoteRestoreSnapshot(ctx, id)
	}
	catalog, err := d.loadSnapshotCatalog()
	if err != nil {
		return d.baseStatus(), err
	}
	item, ok := catalog.find(id)
	if !ok {
		return d.baseStatus(), errors.New("computer snapshot not found")
	}
	if err := d.stop(ctx); err != nil {
		return d.baseStatus(), fmt.Errorf("stop Agent Computer before restore: %w", err)
	}
	if volume := strings.TrimSpace(d.BrowserProfileVolume); volume != "" && item.ProfileVolume != "" {
		if err := d.replaceVolume(ctx, item.ProfileVolume, volume); err != nil {
			return d.baseStatus(), fmt.Errorf("restore browser profile: %w", err)
		}
	}
	catalog.ActiveImage = item.Image
	catalog.ActiveID = item.ID
	if err := d.saveSnapshotCatalog(catalog); err != nil {
		return d.baseStatus(), err
	}
	return d.ensure(ctx)
}

func (d *Docker) DeleteSnapshot(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.remoteEnabled() {
		return d.remoteDeleteSnapshot(ctx, id)
	}
	catalog, err := d.loadSnapshotCatalog()
	if err != nil {
		return err
	}
	item, ok := catalog.find(id)
	if !ok {
		return errors.New("computer snapshot not found")
	}
	next := snapshotCatalog{ActiveImage: catalog.ActiveImage, ActiveID: catalog.ActiveID}
	for _, candidate := range catalog.Snapshots {
		if candidate.ID == id {
			continue
		}
		next.Snapshots = append(next.Snapshots, candidate)
	}
	if catalog.ActiveID == id {
		next.ActiveID = ""
		next.ActiveImage = ""
	}
	if err := d.saveSnapshotCatalog(next); err != nil {
		return err
	}
	if item.Image != "" {
		_ = d.run(ctx, "rmi", "--force", item.Image)
	}
	if item.ProfileVolume != "" {
		_ = d.run(ctx, "volume", "rm", "--force", item.ProfileVolume)
	}
	return nil
}

func (d *Docker) runImage() string {
	catalog, err := d.loadSnapshotCatalog()
	if err != nil {
		return d.Image
	}
	if image := strings.TrimSpace(catalog.ActiveImage); image != "" {
		return image
	}
	return d.Image
}

func (d *Docker) snapshotStatePath() string {
	return filepath.Join(filepath.Dir(d.Workspace), "computer-snapshots.json")
}

func (d *Docker) loadSnapshotCatalog() (snapshotCatalog, error) {
	path := d.snapshotStatePath()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshotCatalog{}, nil
	}
	if err != nil {
		return snapshotCatalog{}, fmt.Errorf("read computer snapshots: %w", err)
	}
	var catalog snapshotCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return snapshotCatalog{}, fmt.Errorf("decode computer snapshots: %w", err)
	}
	return catalog, nil
}

func (d *Docker) saveSnapshotCatalog(catalog snapshotCatalog) error {
	if err := os.MkdirAll(filepath.Dir(d.snapshotStatePath()), 0o700); err != nil {
		return fmt.Errorf("create computer snapshot directory: %w", err)
	}
	encoded, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("encode computer snapshots: %w", err)
	}
	encoded = append(encoded, '\n')
	tmp := d.snapshotStatePath() + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return fmt.Errorf("write computer snapshots: %w", err)
	}
	return os.Rename(tmp, d.snapshotStatePath())
}

func (catalog snapshotCatalog) find(id string) (ComputerSnapshot, bool) {
	for _, item := range catalog.Snapshots {
		if item.ID == id {
			return item, true
		}
	}
	return ComputerSnapshot{}, false
}

func volumeCopyArgs(image, source, destination, script string) []string {
	return []string{
		"run", "--rm", "--entrypoint", "sh",
		"--mount", "type=volume,source=" + source + ",target=/from",
		"--mount", "type=volume,source=" + destination + ",target=/to",
		image, "-c", script,
	}
}

func (d *Docker) cloneVolume(ctx context.Context, source, destination string) error {
	if _, err := d.runOutput(ctx, "volume", "create", destination); err != nil {
		return err
	}
	script := "cd /from && tar cf - . | (cd /to && tar xf -)"
	if _, err := d.runOutputWithTimeout(ctx, 3*time.Minute, volumeCopyArgs(d.Image, source, destination, script)...); err != nil {
		_ = d.run(ctx, "volume", "rm", "--force", destination)
		return err
	}
	return nil
}

func (d *Docker) replaceVolume(ctx context.Context, source, destination string) error {
	_ = d.run(ctx, "volume", "rm", "--force", destination)
	return d.cloneVolume(ctx, source, destination)
}

func (d *Docker) remoteCreateSnapshot(ctx context.Context, name string) (ComputerSnapshot, error) {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return ComputerSnapshot{}, err
	}
	response, err := d.remoteRequest(ctx, http.MethodPost, "/snapshots", body)
	if err != nil {
		return ComputerSnapshot{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return ComputerSnapshot{}, err
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return ComputerSnapshot{}, remoteSnapshotError(raw, response.StatusCode)
	}
	var item ComputerSnapshot
	if err := json.Unmarshal(raw, &item); err != nil {
		return ComputerSnapshot{}, err
	}
	return item, nil
}

func (d *Docker) remoteRestoreSnapshot(ctx context.Context, id string) (Status, error) {
	response, err := d.remoteRequest(ctx, http.MethodPost, "/snapshots/"+id+"/restore", nil)
	if err != nil {
		return d.baseStatus(), err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		return d.baseStatus(), remoteSnapshotError(raw, response.StatusCode)
	}
	var status Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return d.baseStatus(), err
	}
	return status, nil
}

func (d *Docker) remoteDeleteSnapshot(ctx context.Context, id string) error {
	response, err := d.remoteRequest(ctx, http.MethodDelete, "/snapshots/"+id, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		return remoteSnapshotError(raw, response.StatusCode)
	}
	return nil
}

func remoteSnapshotError(raw []byte, status int) error {
	var failure struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &failure)
	if failure.Error != "" {
		return errors.New(failure.Error)
	}
	return fmt.Errorf("remote computer snapshot returned HTTP %d", status)
}

func newSnapshotID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func urlQueryEscape(value string) string {
	return strings.ReplaceAll(value, " ", "%20")
}
