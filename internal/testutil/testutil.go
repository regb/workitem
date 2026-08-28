package testutil

import (
	"bytes"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func ID(t *testing.T, entropyByte byte) string {
	t.Helper()
	id, err := model.NewIDWith(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC), bytes.NewReader(bytes.Repeat([]byte{entropyByte}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func Time() time.Time {
	return time.Date(2026, 7, 30, 19, 30, 0, 0, time.UTC)
}
