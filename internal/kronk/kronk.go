// Package kronk settles group conflicts with a Kronk decision model.
//
// The SDK is pure Go: native llama.cpp libraries load at runtime from the
// Kronk data directory, and models are downloaded (idempotently) on first
// use. This package is only activated by the CLI when --ai is passed.
package kronk

import (
	"context"
	"fmt"
	"time"

	kronksdk "github.com/ardanlabs/kronk/sdk/kronk"

	"photo-library-repair/internal/group"
)

// DefaultModel is the decision model used when --model is not given.
const DefaultModel = "chaoliangUNSW/Jev-Style-0.8B-Decision-v3-Q8_0"

// callTimeout bounds one decision call.
const callTimeout = 5 * time.Minute

// Resolver settles group.Conflict values with a Kronk decision model.
// The ctx passed to New bounds every Resolve call, so cancelling it
// (e.g. on Ctrl-C) stops a running decision.
type Resolver struct {
	krn     *kronksdk.Kronk
	modelID string
	ctx     context.Context
}

// New loads the decision model named modelSource and returns a resolver
// plus a close function that unloads the model.
func New(ctx context.Context, modelSource string) (*Resolver, func(), error) {
	krn, err := loadKronk(ctx, modelSource, "")
	if err != nil {
		return nil, nil, err
	}
	if !krn.ModelInfo().IsDecisionModel {
		_ = krn.Unload(ctx)
		return nil, nil, fmt.Errorf("model %q is not a decision model", modelSource)
	}
	return &Resolver{krn: krn, modelID: krn.ModelID(), ctx: ctx}, func() {
		_ = krn.Unload(context.Background())
	}, nil
}

// Name identifies the resolver; it marks the groups it settles.
func (r *Resolver) Name() string { return r.modelID }

// Resolve asks the decision model to settle one conflict group.
func (r *Resolver) Resolve(c group.Conflict) (group.Decision, error) {
	req := buildRequest(c)
	ctx, cancel := context.WithTimeout(r.ctx, callTimeout)
	defer cancel()
	resp, err := r.krn.Decision(ctx, req)
	if err != nil {
		return group.Decision{}, err
	}
	return applyAnswers(resp, c)
}
