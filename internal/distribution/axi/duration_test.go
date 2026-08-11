package axi

import "testing"

func TestDuration(t *testing.T) {
	for _, test := range []struct {
		milliseconds int64
		want         string
	}{
		{0, "0 ms"},
		{340, "340 ms"},
		{1200, "1.2 s"},
		{25635, "26 s"},
	} {
		if got := Duration(test.milliseconds); got != test.want {
			t.Errorf("Duration(%d) = %q, want %q", test.milliseconds, got, test.want)
		}
	}
}
