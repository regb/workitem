package coordinator

import (
	"fmt"
	"testing"
	"time"
)

func TestJobHistoryBoundsFinishedButKeepsAllUnfinished(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < APIJobHistoryLimit+5; i++ {
		if err := db.SaveAPIJob(fmt.Sprint(i), []byte(fmt.Sprint(i)), time.Unix(int64(i+1), 0)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 7; i++ {
		if err := db.SaveAPIJob(fmt.Sprintf("active-%d", i), []byte("active"), time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := db.APIJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != APIJobHistoryLimit+7 {
		t.Fatal(len(records))
	}
	for _, record := range records {
		for i := 0; i < 5; i++ {
			if string(record) == fmt.Sprint(i) {
				t.Fatalf("retained oldest finished job %d", i)
			}
		}
	}
}
