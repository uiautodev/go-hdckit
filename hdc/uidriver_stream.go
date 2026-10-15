package hdc

import (
	"context"
	"sync"
)

// ScreenStream is a live stream of device screen frames.
type ScreenStream struct {
	driver  *UiDriver
	session uint32
	frames  chan []byte

	stopOnce   sync.Once
	framesOnce sync.Once
}

// Frames returns a channel of raw encoded frames (PNG or JPEG). The channel is
// closed when the stream is stopped or the connection drops. Frames are
// dropped when the consumer falls behind.
func (s *ScreenStream) Frames() <-chan []byte { return s.frames }

func (s *ScreenStream) closeFrames() { s.framesOnce.Do(func() { close(s.frames) }) }

// Close stops the screen stream on the device and releases local resources.
func (s *ScreenStream) Close(ctx context.Context) error {
	var err error
	s.stopOnce.Do(func() {
		err = s.driver.stopCapture(ctx)
	})
	return err
}
