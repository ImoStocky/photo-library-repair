package kronk

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	kronksdk "github.com/ardanlabs/kronk/sdk/kronk"
	"github.com/ardanlabs/kronk/sdk/kronk/model"

	"photo-library-repair/internal/filter"
)

// DefaultVisionModel is used by plr filter classify when --model is omitted.
const DefaultVisionModel = "unsloth/Qwen3-VL-4B-Instruct-BF16"

const classifyTimeout = 5 * time.Minute
const classifyMaxSide = 1024 // used for ImageTokenBudget; pixels are not re-encoded

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
// Later calls keep that system prompt and send only the image file bytes
// (no decode/re-encode).
func (c *VisionClassifier) Classify(ctx context.Context, imagePath, prompt string, labels []string) (filter.ClassifyResult, error) {
	_ = labels // already embedded in prompt by filter.BuildClassifyPrompt
	c.sysOnce.Do(func() { c.system = strings.TrimSpace(prompt) })
	if c.system == "" {
		return filter.ClassifyResult{}, fmt.Errorf("empty classify system prompt")
	}
	data, mime, err := readImageFile(imagePath)
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
		"messages":        buildClassifyMessages(c.system, data, mime),
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
func buildClassifyMessages(systemPrompt string, data []byte, mime string) []model.D {
	return model.Messages(
		model.TextMessage(model.RoleSystem, systemPrompt),
		imageOnlyUserMessage(data, mime),
	)
}

func imageOnlyUserMessage(data []byte, mime string) model.D {
	if mime == "" {
		mime = "jpeg"
	}
	url := "data:image/" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
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

// readImageFile returns the on-disk bytes and a mime subtype (jpeg/png/webp/…)
// without decoding or re-encoding the image.
func readImageFile(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return data, imageMIME(path, data), nil
}

func imageMIME(path string, data []byte) string {
	if m := mimeFromMagic(data); m != "" {
		return m
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "jpeg"
	case ".png":
		return "png"
	case ".webp":
		return "webp"
	case ".gif":
		return "gif"
	case ".heic", ".heif":
		return "heic"
	default:
		return "jpeg"
	}
}

func mimeFromMagic(data []byte) string {
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "jpeg"
	}
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "png"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "webp"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "gif"
	}
	return ""
}
