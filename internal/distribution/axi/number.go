package axi

import "strconv"

// Number formats an integer for human output. Machine envelopes keep their
// numeric JSON values; only renderers call this helper.
func Number(value int64) string {
	digits := strconv.FormatInt(value, 10)
	sign := ""
	if digits[0] == '-' {
		sign, digits = "-", digits[1:]
	}
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return sign + digits
}
