package addressbloomfilter

import "testing"

func TestBloomReplyTrue_AcceptsResp2AndResp3Replies(t *testing.T) {
	cases := []struct {
		item any
		want bool
	}{
		{int64(1), true},  // RESP2
		{int64(0), false}, // RESP2
		{true, true},      // RESP3
		{false, false},    // RESP3
		{"1", false},
		{nil, false},
	}
	for _, c := range cases {
		if got := bloomReplyTrue(c.item); got != c.want {
			t.Errorf("bloomReplyTrue(%#v) = %v, want %v", c.item, got, c.want)
		}
	}
}
