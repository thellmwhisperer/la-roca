//go:build acceptance

package acceptance

func intValue(value any) int {
	number, _ := value.(float64)
	return int(number)
}

const providerDeadEndpoint = "http://127.0.0.1:1"
