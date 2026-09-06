package httpapi

import "os"

func openTaskArtifactFile(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}
