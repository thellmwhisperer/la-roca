package axi

import "testing"

func TestNumberSeparatesThousandsForPeople(t *testing.T) {
	for _, test := range []struct {
		value int64
		want  string
	}{
		{0, "0"}, {999, "999"}, {1000, "1,000"},
		{178579, "178,579"}, {-1234567, "-1,234,567"},
	} {
		if got := Number(test.value); got != test.want {
			t.Errorf("Number(%d) = %q, want %q", test.value, got, test.want)
		}
	}
}
