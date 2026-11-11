//go:build !darwin

package apply

import "time"

func setBirthTime(string, time.Time) error { return nil }
