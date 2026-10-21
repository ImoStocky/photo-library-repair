package kronk

import (
	"fmt"

	"github.com/ardanlabs/kronk/sdk/kronk/model"

	"photo-library-repair/internal/group"
)

// memberFact is one group member as rendered into the decision state.
type memberFact struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Size        int64   `json:"size"`
	ExifCapture string  `json:"exif_capture,omitzero"`
	ExifOffset  string  `json:"exif_offset,omitzero"`
	HasGPS      bool    `json:"has_gps"`
	GPSLat      float64 `json:"gps_lat,omitzero"`
	GPSLon      float64 `json:"gps_lon,omitzero"`
	CreatedAt   int64   `json:"created_at"`
}

func facts(imgs []group.Image) []memberFact {
	out := make([]memberFact, len(imgs))
	for i, im := range imgs {
		out[i] = memberFact{
			ID: im.ID, Name: im.Name, Kind: im.Kind,
			Width: im.Width, Height: im.Height, Size: im.Size,
			ExifCapture: im.ExifCapture, ExifOffset: im.ExifOffset,
			HasGPS: im.HasGPS, GPSLat: im.GPSLat, GPSLon: im.GPSLon,
			CreatedAt: im.CreatedAt,
		}
	}
	return out
}

// buildRequest renders a conflict as one decision request: shared state
// (reason + every member's facts) and one choice question per plan field.
func buildRequest(c group.Conflict) model.DecisionRequest {
	n := len(c.Images)
	pixelOpts, captureOpts, nameOpts := make([]model.DecisionOption, 0, n), make([]model.DecisionOption, 0, n), make([]model.DecisionOption, 0, n)
	for _, im := range c.Images {
		desc := fmt.Sprintf("file %q: %dx%d %s, %d bytes, seen %d, exif %q %s, gps=%v",
			im.Name, im.Width, im.Height, im.Kind, im.Size, im.CreatedAt, im.ExifCapture, im.ExifOffset, im.HasGPS)
		pixelOpts = append(pixelOpts, model.DecisionQuestionOpt(im.ID, desc))
		captureOpts = append(captureOpts, model.DecisionQuestionOpt(im.ID, desc))
		nameOpts = append(nameOpts, model.DecisionQuestionOpt(im.Name, desc))
	}
	return model.DecisionRequest{
		State: model.DecisionState(
			model.DecisionStateData("reason", c.Reason),
			model.DecisionStateData("members", facts(c.Images)),
		),
		Questions: []model.DecisionQuestion{
			model.DecisionQuestionChoice("pixels",
				"Which file should provide the stored pixels? Prefer the highest resolution, then the best encoding, then the largest file.",
				pixelOpts...),
			model.DecisionQuestionChoice("capture",
				"Which file's capture time should the library keep?",
				captureOpts...),
			model.DecisionQuestionChoice("name",
				"Which file name should the library store?",
				nameOpts...),
		},
	}
}
