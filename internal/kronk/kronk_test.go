package kronk

import (
	"testing"

	"github.com/ardanlabs/kronk/sdk/kronk/model"

	"photo-library-repair/internal/group"
)

func testConflict() group.Conflict {
	a := group.Image{ID: "a", Name: "IMG_0001.jpg", Kind: "jpeg", Width: 320, Height: 240, Size: 100,
		ExifCapture: "2024:05:01 12:30:00", ExifOffset: "+02:00", HasGPS: true, GPSLat: 52.52, GPSLon: 13.40, CreatedAt: 1}
	b := group.Image{ID: "b", Name: "IMG_0001 (1).jpg", Kind: "jpeg", Width: 320, Height: 240, Size: 80,
		ExifCapture: "2024:05:01 12:31:00", ExifOffset: "+02:00", CreatedAt: 2}
	return group.Conflict{Reason: "EXIF GPS positions differ", Images: []group.Image{a, b}}
}

func TestBuildRequest(t *testing.T) {
	req := buildRequest(testConflict())
	if len(req.Questions) != 3 {
		t.Fatalf("want 3 questions, got %d", len(req.Questions))
	}
	want := map[string]int{"pixels": 2, "capture": 2, "name": 2}
	for _, q := range req.Questions {
		if q.Type != model.DecisionQuestionTypeChoice {
			t.Fatalf("question %q type = %v", q.ID, q.Type)
		}
		if len(q.Options) != want[q.ID] {
			t.Fatalf("question %q has %d options, want %d", q.ID, len(q.Options), want[q.ID])
		}
	}
}

func TestApplyAnswers(t *testing.T) {
	c := testConflict()
	resp := model.DecisionResponse{Answers: map[string]model.DecisionAnswer{
		"pixels":  {Type: model.DecisionQuestionTypeChoice, Choice: "b"},
		"capture": {Type: model.DecisionQuestionTypeChoice, Choice: "a"},
		"name":    {Type: model.DecisionQuestionTypeChoice, Choice: "IMG_0001.jpg"},
	}}
	d, err := applyAnswers(resp, c)
	if err != nil {
		t.Fatal(err)
	}
	if d.PixelImageID != "b" || d.CaptureImageID != "a" || d.StoredName != "IMG_0001.jpg" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestApplyAnswersRejectsUnknownMember(t *testing.T) {
	c := testConflict()
	resp := model.DecisionResponse{Answers: map[string]model.DecisionAnswer{
		"pixels":  {Type: model.DecisionQuestionTypeChoice, Choice: "zzz"},
		"capture": {Type: model.DecisionQuestionTypeChoice, Choice: "a"},
		"name":    {Type: model.DecisionQuestionTypeChoice, Choice: "IMG_0001.jpg"},
	}}
	if _, err := applyAnswers(resp, c); err == nil {
		t.Fatal("unknown member must be rejected")
	}
	// A file name that no member carries must be rejected too.
	resp.Answers["pixels"] = model.DecisionAnswer{Type: model.DecisionQuestionTypeChoice, Choice: "a"}
	resp.Answers["name"] = model.DecisionAnswer{Type: model.DecisionQuestionTypeChoice, Choice: "nope.jpg"}
	if _, err := applyAnswers(resp, c); err == nil {
		t.Fatal("foreign file name must be rejected")
	}
}

func TestApplyAnswersRejectsMissingAnswer(t *testing.T) {
	c := testConflict()
	resp := model.DecisionResponse{Answers: map[string]model.DecisionAnswer{
		"pixels": {Type: model.DecisionQuestionTypeChoice, Choice: "a"},
	}}
	if _, err := applyAnswers(resp, c); err == nil {
		t.Fatal("missing answer must be rejected")
	}
}
