package utils

import (
	"fmt"
	"image"
	"io"
)

// EnsureImageDimensionsBounded reads the image header from r, rewinds r to its
// start, and returns an error when the declared dimensions are invalid or
// their product exceeds maxPixels. Call it before image.Decode so a small file
// declaring huge dimensions (a pixel bomb) is rejected before the full decode
// allocates memory proportional to those dimensions.
func EnsureImageDimensionsBounded(r io.ReadSeeker, maxPixels int) error {
	config, _, err := image.DecodeConfig(r)
	if err != nil {
		return fmt.Errorf("read image dimensions: %w", err)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind image after dimension check: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxPixels/config.Height {
		return fmt.Errorf("image dimensions %dx%d exceed the %d pixel limit", config.Width, config.Height, maxPixels)
	}
	return nil
}
