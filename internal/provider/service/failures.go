package service

const (
	// DegradedUnavailable: no provider of the order was available.
	DegradedUnavailable = "model_unavailable"
	// DegradedLLMError: a provider said it was available and then failed.
	DegradedLLMError = "model_error"
	// DegradedInvalidSQL: the model answered and the gate rejected it.
	DegradedInvalidSQL = "invalid_sql"
	// DegradedExecution: the SQL passed the gate and blew up when it ran.
	DegradedExecution = "sql_execution_error"
	// DegradedTimeout: the SQL passed the gate but exceeded its configured work budget.
	DegradedTimeout = "sql_execution_timeout"
)

// IsDegradedFailure is the one success contract shared by CLI exit codes and
// MCP tool results. A rescue may still carry useful rows, but these modes mean
// the model-backed operation failed.
func IsDegradedFailure(mode string) bool {
	return mode == DegradedUnavailable || mode == DegradedLLMError ||
		mode == DegradedInvalidSQL || mode == DegradedExecution || mode == DegradedTimeout
}
