package kronk

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"strings"
	"sync"
	"time"

	kronksdk "github.com/ardanlabs/kronk/sdk/kronk"
	"github.com/ardanlabs/kronk/sdk/kronk/model"
	_ "github.com/gen2brain/heic"
	_ "golang.org/x/image/webp"

	"photo-library-repair/internal/filter"
)

// DefaultVisionModel is used by plr filter classify when --model is omitted.
const DefaultVisionModel = "unsloth/Qwen3-VL-4B-Instruct-BF16"

const classifyTimeout = 5 * time.Minute
const classifyMaxSide = 1024

// VisionClassifier labels images via Kronk Chat.
// The classify rule text is pinned once as a system prompt; each Classify
// call sends only a new image user turn and expects a label reply.
type VisionClassifier struct {
	krn     *kronksdk.Kronk
	modelID string
	ctx     context.Context

	sysOnce sync.Once
	system  string
}

// NewVisionClassifier loads a vision (projection) model for classify.
// projFile, when non-empty, overrides the downloaded mmproj (e.g. a Q8_0
// projector paired with Q4 text weights).
func NewVisionClassifier(ctx context.Context, modelSource, projFile string) (*VisionClassifier, func(), error) {
	krn, err := loadKronk(ctx, modelSource, projFile)
	if err != nil {
		return nil, nil, err
	}
	if !krn.ModelInfo().HasProjection {
		_ = krn.Unload(ctx)
		return nil, nil, fmt.Errorf("model %q has no vision projection", modelSource)
	}
	return &VisionClassifier{krn: krn, modelID: krn.ModelID(), ctx: ctx}, func() {
		_ = krn.Unload(context.Background())
	}, nil
}

// Name identifies the loaded vision model.
func (c *VisionClassifier) Name() string { return c.modelID }

// Classify implements filter.Classifier.
//
// The first call pins prompt (rule text + labels) as the system setup.
// Later calls keep that system prompt and send only the image.
func (c *VisionClassifier) Classify(ctx context.Context, imagePath, prompt string, labels []string) (filter.ClassifyResult, error) {
	_ = labels // already embedded in prompt by filter.BuildClassifyPrompt
	c.sysOnce.Do(func() { c.system = strings.TrimSpace(prompt) })
	if c.system == "" {
		return filter.ClassifyResult{}, fmt.Errorf("empty classify system prompt")
	}
	jpegBytes, err := imageJPEGBytes(imagePath, classifyMaxSide)
	if err != nil {
		return filter.ClassifyResult{}, err
	}
	if ctx == nil {
		ctx = c.ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, classifyTimeout)
	defer cancel()
	resp, err := c.krn.Chat(callCtx, model.D{
		"messages":        buildClassifyMessages(c.system, jpegBytes),
		"max_tokens":      128,
		"enable_thinking": false,
		"temperature":     0.0,
	})
	if err != nil {
		return filter.ClassifyResult{}, err
	}
	raw := ""
	if len(resp.Choices) > 0 && resp.Choices[0].Message != nil {
		raw = resp.Choices[0].Message.Content
	}
	return filter.ClassifyResult{Raw: raw, Label: strings.TrimSpace(raw)}, nil
}

// buildClassifyMessages is system setup + image-only user turn.
func buildClassifyMessages(systemPrompt string, jpeg []byte) []model.D {
	return model.Messages(
		model.TextMessage(model.RoleSystem, systemPrompt),
		imageOnlyUserMessage(jpeg),
	)
}

func imageOnlyUserMessage(jpeg []byte) model.D {
	url := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)
	return model.D{
		"role": model.RoleUser,
		"content": []model.D{
			{
				"type": "image_url",
				"image_url": model.D{
					"url": url,
				},
			},
		},
	}
}

func imageJPEGBytes(path string, maxSide int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxSide > 0 && (w > maxSide || h > maxSide) {
		img = downscale(img, maxSide)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func downscale(img image.Image, maxSide int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := float64(maxSide) / float64(max(w, h))
	nw := max(1, int(float64(w)*scale))
	nh := max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := range nh {
		for x := range nw {
			sx := b.Min.X + x*w/nw
			sy := b.Min.Y + y*h/nh
			dst.Set(x, y, img.At(sx, sy))
		}
	}
	return dst
}
