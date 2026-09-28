package win

import "testing"

func TestMinimumOuterSizeIncludesFrameAndDPI(t *testing.T) {
	outer := rect{Left: 100, Top: 200, Right: 1200, Bottom: 960}
	client := rect{Right: 1084, Bottom: 721}
	for _, tc := range []struct {
		dpi  int
		want point
	}{
		{96, point{X: 976, Y: 679}},
		{120, point{X: 1216, Y: 839}},
		{137, point{X: 1386, Y: 953}},
		{144, point{X: 1456, Y: 999}},
	} {
		if got := minimumOuterSize(960, 640, tc.dpi, outer, client); got != tc.want {
			t.Errorf("%d dpi: got %+v, want %+v", tc.dpi, got, tc.want)
		}
	}
}
