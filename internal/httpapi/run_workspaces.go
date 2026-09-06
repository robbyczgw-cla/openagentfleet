package httpapi

import (
	"context"
	"fmt"
	"os"
)

func (s *Server) runWorkdir(ctx context.Context, runID string) (string, error) {
	if s.Store == nil {
		return s.HarnessWorkdir, nil
	}
	workdir, err := s.Store.GetRunWorkdir(ctx, runID)
	if err != nil {
		return "", err
	}
	if workdir == "" {
		return s.HarnessWorkdir, nil
	}
	info, err := os.Stat(workdir)
	if err != nil {
		return "", fmt.Errorf("run working directory unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("run working directory is not a directory")
	}
	return workdir, nil
}
