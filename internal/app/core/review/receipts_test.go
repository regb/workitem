package review

import (
	"fmt"
	"testing"
)

func TestReviewRetainsOnlyMostRecentTwoHundredReceipts(t *testing.T) {
	s := New("item", "repo")
	for i := 0; i < 250; i++ {
		s.RememberOperation(fmt.Sprint(i), Receipt{Digest: fmt.Sprint(i), Revision: "r1"})
	}
	if len(s.Operations) != 200 || len(s.OperationOrder) != 200 {
		t.Fatal("unbounded receipts")
	}
	if _, ok := s.Operations["0"]; ok {
		t.Fatal("old receipt retained")
	}
	if s.Operations["249"].Digest != "249" {
		t.Fatal("recent retry lost")
	}
	s.RememberOperation("100", Receipt{Digest: "same"})
	if len(s.OperationOrder) != 200 {
		t.Fatal("retry grew receipt order")
	}
}
