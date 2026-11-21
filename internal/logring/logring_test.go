package logring

import (
	"fmt"
	"slices"
	"testing"
)

func TestRing(t *testing.T) {
	r := New(3)
	if got := r.Last(10); len(got) != 0 {
		t.Fatalf("empty ring: %v", got)
	}
	fmt.Fprint(r, "a\nb")
	fmt.Fprint(r, "c\n")
	if got := r.Last(10); !slices.Equal(got, []string{"a", "bc"}) {
		t.Fatalf("partial lines: %v", got)
	}
	for i := 0; i < 5; i++ {
		fmt.Fprintf(r, "l%d\n", i)
	}
	if got := r.Last(10); !slices.Equal(got, []string{"l2", "l3", "l4"}) {
		t.Fatalf("wrapped: %v", got)
	}
	if got := r.Last(2); !slices.Equal(got, []string{"l3", "l4"}) {
		t.Fatalf("last 2: %v", got)
	}
}
