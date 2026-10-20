package gateway

import (
	"reflect"
	"testing"

	"github.com/openabstractions/abstraction-asks/go/pendingquestioncases"
)

func TestPendingQuestionWordCorpus(t *testing.T) {
	cases := pendingquestioncases.Cases()
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			if got := QuestionsPending(tc.Reason); !reflect.DeepEqual(got, tc.IDs) {
				t.Fatalf("got %v, want %v", got, tc.IDs)
			}
		})
	}
}
