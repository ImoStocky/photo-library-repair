package kronk

import (
	"fmt"

	"github.com/ardanlabs/kronk/sdk/kronk/model"

	"photo-library-repair/internal/group"
)

// applyAnswers maps decision answers back onto a group decision, rejecting
// any choice that does not name a member of the group.
func applyAnswers(resp model.DecisionResponse, c group.Conflict) (group.Decision, error) {
	ids, names := make(map[string]bool, len(c.Images)), make(map[string]bool, len(c.Images))
	for _, im := range c.Images {
		ids[im.ID] = true
		names[im.Name] = true
	}
	pick := func(set map[string]bool, q, what string) (string, error) {
		a, ok := resp.Answers[q]
		if !ok {
			return "", fmt.Errorf("no answer for question %q", q)
		}
		if !set[a.Choice] {
			return "", fmt.Errorf("%s answer %q is not a member of the group", what, a.Choice)
		}
		return a.Choice, nil
	}
	pixelID, err := pick(ids, "pixels", "pixels")
	if err != nil {
		return group.Decision{}, err
	}
	captureID, err := pick(ids, "capture", "capture")
	if err != nil {
		return group.Decision{}, err
	}
	name, err := pick(names, "name", "name")
	if err != nil {
		return group.Decision{}, err
	}
	return group.Decision{PixelImageID: pixelID, CaptureImageID: captureID, StoredName: name}, nil
}
