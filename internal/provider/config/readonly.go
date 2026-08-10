package config

import "strings"

// EnvReadOnly is the operator's switch for read-only mode, with the lab's own
// spelling so a machine that already exports it keeps working.
const EnvReadOnly = "ROCA_READ_ONLY"

// falseWords are the spellings that mean "off". Everything else that is not
// empty means on: somebody who writes the variable is asking for something, and
// reading a typo as "off" would license exactly the writes they were forbidding.
var falseWords = map[string]bool{"": true, "0": true, "false": true, "no": true, "off": true}

// ReadOnly says whether this invocation may write, given what the environment
// declared.
func ReadOnly(value string) bool {
	return !falseWords[strings.ToLower(strings.TrimSpace(value))]
}
