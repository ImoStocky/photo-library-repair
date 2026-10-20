// Command plr is a photo library repair tool.
//
// It scans directories, fingerprints every image (sha256 + 16-bit
// perceptual hash + EXIF capture time), groups near-duplicate pictures,
// resolves each group into a plan (earliest EXIF capture time paired with
// the best quality pixels, GPS attached), and can apply the plan on demand
// by copying the chosen files into a target directory with merged metadata.
package main

import (
	"os"

	"photo-library-repair/internal/cli"
)

func main() { os.Exit(cli.Run(os.Args[1:])) }
