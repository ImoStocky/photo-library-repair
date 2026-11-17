package kronk

import (
	"context"
	"fmt"
	"os"
	"runtime"

	kronksdk "github.com/ardanlabs/kronk/sdk/kronk"
	"github.com/ardanlabs/kronk/sdk/kronk/model"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
	"github.com/ardanlabs/kronk/sdk/tools/models"
)

// resolveProjFile picks the mmproj path Kronk should load.
// An empty override keeps the downloaded projector; a non-empty override
// must exist on disk (used to force a lower-quant vision encoder).
func resolveProjFile(downloaded, override string) (string, error) {
	if override == "" {
		return downloaded, nil
	}
	if _, err := os.Stat(override); err != nil {
		return "", fmt.Errorf("mmproj %q: %w", override, err)
	}
	return override, nil
}

// loadKronk installs the host llama runtime, inits the SDK, downloads
// modelSource, and returns a loaded Kronk handle.
//
// Primary path matches Kronk examples / chapter 20:
//
//	libs.New(WithDetect) → Download → Init(WithLibPath) → models.Download → New
//
// On darwin, if that Metal-preferred detect path fails before or during load,
// retry once with an explicit CPU processor (NGpuLayers 0). Init is
// process-wide: a second Init after a successful Metal Init is a no-op.
//
// projOverride, when non-empty, replaces the mmproj Kronk would otherwise
// download with the model (see resolveProjFile).
func loadKronk(ctx context.Context, modelSource, projOverride string) (*kronksdk.Kronk, error) {
	krn, err := loadKronkOnce(ctx, modelSource, projOverride, "" /* detect */, false)
	if err == nil {
		return krn, nil
	}
	if runtime.GOOS != "darwin" || os.Getenv("KRONK_PROCESSOR") != "" {
		return nil, err
	}

	fmt.Fprintf(os.Stderr, "kronk: detect/preferred runtime failed (%v); falling back to cpu\n", err)
	krn, err2 := loadKronkOnce(ctx, modelSource, projOverride, "cpu", true)
	if err2 != nil {
		return nil, fmt.Errorf("kronk: preferred failed (%v); cpu fallback failed: %w", err, err2)
	}
	return krn, nil
}

func loadKronkOnce(ctx context.Context, modelSource, projOverride, processor string, cpuOnly bool) (*kronksdk.Kronk, error) {
	libOpts := []libs.Option{libs.WithValidation(true)}
	if processor == "" {
		libOpts = append(libOpts, libs.WithDetect(ctx, kronksdk.FmtLogger))
	} else {
		libOpts = append(libOpts, libs.WithDetectOverrides(ctx, kronksdk.FmtLogger, "", "", "", processor))
	}
	lib, err := libs.New(libOpts...)
	if err != nil {
		return nil, fmt.Errorf("kronk libs: %w", err)
	}
	if _, err := lib.Download(ctx, kronksdk.FmtLogger); err != nil {
		return nil, fmt.Errorf("kronk libs download: %w", err)
	}
	if err := kronksdk.Init(kronksdk.WithLibPath(lib.LibsPath())); err != nil {
		return nil, fmt.Errorf("kronk init: %w", err)
	}

	mdls, err := models.New()
	if err != nil {
		return nil, fmt.Errorf("kronk models: %w", err)
	}
	mp, err := mdls.Download(ctx, kronksdk.FmtLogger, modelSource)
	if err != nil {
		return nil, fmt.Errorf("kronk model download: %w", err)
	}
	proj, err := resolveProjFile(mp.ProjFile, projOverride)
	if err != nil {
		return nil, err
	}
	if projOverride != "" {
		fmt.Fprintf(os.Stderr, "kronk: mmproj override %s\n", proj)
	}

	opts := []model.Option{
		model.WithModelFiles(mp.ModelFiles),
		model.WithProjFile(proj),
		model.WithAutoTune(true),
	}
	if cpuOnly {
		opts = append(opts, model.WithNGpuLayers(0))
	}
	krn, err := kronksdk.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("kronk model load: %w", err)
	}
	return krn, nil
}
