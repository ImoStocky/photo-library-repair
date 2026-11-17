package filter

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// Matcher tests one ImageInfo.
type Matcher func(*ImageInfo) bool

func compileMatch(m Match, ruleName string) (Matcher, bool, error) {
	var parts []Matcher
	needsEXIF := false

	for key, node := range m.Conds {
		fn, exif, err := compileCond(key, node, ruleName)
		if err != nil {
			return nil, false, err
		}
		if exif {
			needsEXIF = true
		}
		parts = append(parts, fn)
	}
	for _, kid := range m.All {
		fn, exif, err := compileMatch(kid, ruleName)
		if err != nil {
			return nil, false, err
		}
		if exif {
			needsEXIF = true
		}
		parts = append(parts, fn)
	}
	if len(m.Any) > 0 {
		var anyParts []Matcher
		for _, kid := range m.Any {
			fn, exif, err := compileMatch(kid, ruleName)
			if err != nil {
				return nil, false, err
			}
			if exif {
				needsEXIF = true
			}
			anyParts = append(anyParts, fn)
		}
		parts = append(parts, func(info *ImageInfo) bool {
			for _, fn := range anyParts {
				if fn(info) {
					return true
				}
			}
			return false
		})
	}
	if m.Not != nil {
		fn, exif, err := compileMatch(*m.Not, ruleName)
		if err != nil {
			return nil, false, err
		}
		if exif {
			needsEXIF = true
		}
		parts = append(parts, func(info *ImageInfo) bool { return !fn(info) })
	}

	if len(parts) == 0 {
		return func(*ImageInfo) bool { return true }, needsEXIF, nil
	}
	return func(info *ImageInfo) bool {
		for _, fn := range parts {
			if !fn(info) {
				return false
			}
		}
		return true
	}, needsEXIF, nil
}

func compileCond(key string, node yaml.Node, ruleName string) (Matcher, bool, error) {
	line := node.Line
	prefix := fmt.Sprintf("filter rules: rule %q line %d", ruleName, line)

	switch {
	case key == "format":
		vals, err := stringList(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: format: %w", prefix, err)
		}
		set := map[string]bool{}
		for _, v := range vals {
			set[strings.ToLower(v)] = true
		}
		return func(info *ImageInfo) bool { return set[strings.ToLower(info.Format)] }, false, nil

	case key == "filename":
		pat, err := scalarString(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: filename: %w", prefix, err)
		}
		return func(info *ImageInfo) bool {
			ok, _ := doublestar.Match(pat, filepath.Base(info.Path))
			return ok
		}, false, nil

	case key == "path":
		pat, err := scalarString(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: path: %w", prefix, err)
		}
		return func(info *ImageInfo) bool {
			p := filepath.ToSlash(info.RelPath)
			if p == "" {
				p = filepath.ToSlash(info.Path)
			}
			ok, _ := doublestar.Match(pat, p)
			return ok
		}, false, nil

	case key == "min_width":
		n, err := intScalar(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: min_width: %w", prefix, err)
		}
		return func(info *ImageInfo) bool { return info.Width >= n }, false, nil
	case key == "max_width":
		n, err := intScalar(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: max_width: %w", prefix, err)
		}
		return func(info *ImageInfo) bool { return info.Width <= n }, false, nil
	case key == "min_height":
		n, err := intScalar(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: min_height: %w", prefix, err)
		}
		return func(info *ImageInfo) bool { return info.Height >= n }, false, nil
	case key == "max_height":
		n, err := intScalar(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: max_height: %w", prefix, err)
		}
		return func(info *ImageInfo) bool { return info.Height <= n }, false, nil

	case key == "min_file_size":
		n, err := parseSizeNode(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: min_file_size: %w", prefix, err)
		}
		return func(info *ImageInfo) bool { return info.Size >= n }, false, nil
	case key == "max_file_size":
		n, err := parseSizeNode(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: max_file_size: %w", prefix, err)
		}
		return func(info *ImageInfo) bool { return info.Size <= n }, false, nil

	case key == "aspect_ratio":
		v, err := floatScalar(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: aspect_ratio: %w", prefix, err)
		}
		return func(info *ImageInfo) bool {
			r := aspectRatio(info.Width, info.Height)
			return abs(r-v) <= 0.02
		}, false, nil
	case key == "min_aspect_ratio":
		v, err := floatScalar(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: min_aspect_ratio: %w", prefix, err)
		}
		return func(info *ImageInfo) bool { return aspectRatio(info.Width, info.Height) >= v }, false, nil
	case key == "max_aspect_ratio":
		v, err := floatScalar(node)
		if err != nil {
			return nil, false, fmt.Errorf("%s: max_aspect_ratio: %w", prefix, err)
		}
		return func(info *ImageInfo) bool { return aspectRatio(info.Width, info.Height) <= v }, false, nil

	case strings.HasPrefix(key, "exif."):
		tag := strings.TrimPrefix(key, "exif.")
		if tag == "" {
			return nil, false, fmt.Errorf("%s: empty exif tag", prefix)
		}
		wantNull := node.Tag == "!!null" || (node.Kind == yaml.ScalarNode && (node.Value == "null" || node.Value == "~"))
		var want string
		if !wantNull {
			s, err := scalarString(node)
			if err != nil {
				return nil, false, fmt.Errorf("%s: %s: %w", prefix, key, err)
			}
			want = s
		}
		return func(info *ImageInfo) bool {
			val, ok := info.EXIF(tag)
			if wantNull {
				return !ok
			}
			return ok && val == want
		}, true, nil
	}

	return nil, false, fmt.Errorf("%s: unknown match key %q", prefix, key)
}

func aspectRatio(w, h int) float64 {
	if w <= 0 || h <= 0 {
		return 0
	}
	a, b := float64(w), float64(h)
	if a < b {
		a, b = b, a
	}
	return a / b
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func stringList(node yaml.Node) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		s, err := scalarString(node)
		if err != nil {
			return nil, err
		}
		return []string{s}, nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(node.Content))
		for _, c := range node.Content {
			s, err := scalarString(*c)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("want string or list, got %s", node.Tag)
	}
}

func scalarString(node yaml.Node) (string, error) {
	if node.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("want string, got %s", node.ShortTag())
	}
	return node.Value, nil
}

func intScalar(node yaml.Node) (int, error) {
	s, err := scalarString(node)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("want int: %w", err)
	}
	return n, nil
}

func floatScalar(node yaml.Node) (float64, error) {
	s, err := scalarString(node)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("want number: %w", err)
	}
	return v, nil
}

func parseSizeNode(node yaml.Node) (int64, error) {
	s, err := scalarString(node)
	if err != nil {
		return 0, err
	}
	return ParseSize(s)
}

// ParseSize parses a byte size: plain int, or human units with KB/MB/GB (1024).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	upper := strings.ToUpper(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(upper, "KB"):
		mult = 1024
		s = strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(upper, "MB"):
		mult = 1024 * 1024
		s = strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(upper, "GB"):
		mult = 1024 * 1024 * 1024
		s = strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(upper, "B") && !strings.HasSuffix(upper, "KB") && !strings.HasSuffix(upper, "MB") && !strings.HasSuffix(upper, "GB"):
		s = strings.TrimSpace(s[:len(s)-1])
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unparseable size %q", s)
	}
	return n * mult, nil
}
