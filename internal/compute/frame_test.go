package compute

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func encodeTestFrame(t *testing.T, fill color.Color, highlight bool) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < canvas.Bounds().Dy(); y++ {
		for x := 0; x < canvas.Bounds().Dx(); x++ {
			canvas.Set(x, y, fill)
		}
	}
	if highlight {
		for y := 2; y < 14; y++ {
			for x := 2; x < 14; x++ {
				canvas.Set(x, y, color.White)
			}
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestValidateDesktopFrameRejectsBlankCapture(t *testing.T) {
	frame := encodeTestFrame(t, color.RGBA{R: 35, G: 34, B: 31, A: 255}, false)
	if err := validateDesktopFrame(frame); err == nil {
		t.Fatal("blank desktop capture was accepted")
	}
}

func TestValidateDesktopFrameAcceptsVisibleDesktop(t *testing.T) {
	frame := encodeTestFrame(t, color.RGBA{R: 35, G: 34, B: 31, A: 255}, true)
	if err := validateDesktopFrame(frame); err != nil {
		t.Fatalf("visible desktop capture was rejected: %v", err)
	}
}
