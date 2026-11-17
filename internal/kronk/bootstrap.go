package kronk

import (
	"context"
	"fmt"

	kronksdk "github.com/ardanlabs/kronk/sdk/kronk"
	"github.com/ardanlabs/kronk/sdk/kronk/model"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
	"github.com/ardanlabs/kronk/sdk/tools/models"
)

// loadKronk downloads libs + modelSource and returns a loaded Kronk handle.
func loadKronk(ctx context.Context, modelSource string) (*kronksdk.Kronk, error) {
	lib, err := libs.New(
		libs.WithDetect(ctx, kronksdk.FmtLogger),
		libs.WithValidation(true),
	)
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
	krn, err := kronksdk.New(
		model.WithModelFiles(mp.ModelFiles),
		model.WithProjFile(mp.ProjFile),
		model.WithAutoTune(true),
	)
	if err != nil {
		return nil, fmt.Errorf("kronk model load: %w", err)
	}
	return krn, nil
}
