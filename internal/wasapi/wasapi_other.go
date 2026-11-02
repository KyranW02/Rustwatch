//go:build !windows

package wasapi

// Capture is not available off Windows.
func Capture(stop <-chan struct{}, pid uint32, allowSystem bool, on OnAudio, started func(Mode)) error {
	return ErrUnsupported
}
