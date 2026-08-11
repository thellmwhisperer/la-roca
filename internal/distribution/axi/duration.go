package axi

import "fmt"

// Duration formats milliseconds with only the precision a person can use.
func Duration(milliseconds int64) string {
	switch {
	case milliseconds < 1000:
		return fmt.Sprintf("%d ms", milliseconds)
	case milliseconds < 10000:
		return fmt.Sprintf("%.1f s", float64(milliseconds)/1000)
	default:
		return fmt.Sprintf("%.0f s", float64(milliseconds)/1000)
	}
}
