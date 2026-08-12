package logfile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// The declared vocabulary of error_type. It is a consumer contract, so it names
// the kind of failure in the product's own terms; the Go type that carried the
// error is an implementation detail and changing it must not change the log.
const (
	// ErrorCanceled: the caller stopped the call before it finished.
	ErrorCanceled = "canceled"
	// ErrorTimeout: the call ran past its deadline.
	ErrorTimeout = "timeout"
	// ErrorNotFound: something the call needs is not on this machine.
	ErrorNotFound = "not_found"
	// ErrorPermission: the operating system refused access to it.
	ErrorPermission = "permission_denied"
	// ErrorAlreadyExists: what the call would create is already there.
	ErrorAlreadyExists = "already_exists"
	// ErrorInvalidUsage: the invocation itself was wrong.
	ErrorInvalidUsage = "invalid_usage"
	// ErrorNotInitialized: this installation has no database yet.
	ErrorNotInitialized = "not_initialized"
	// ErrorCommandFailure: a command exited non-zero without an error value.
	ErrorCommandFailure = "command_failure"
	// ErrorToolError: an MCP tool failed without a declared reason.
	ErrorToolError = "tool_error"
	// ErrorUnclassified: a failure this build does not categorize yet.
	ErrorUnclassified = "unclassified_error"
)

type correlatedError struct {
	err error
	id  string
}

func (e correlatedError) Error() string {
	return fmt.Sprintf("%v (correlation_id: %s)", e.err, e.id)
}

func (e correlatedError) Unwrap() error { return e.err }

func (e correlatedError) CorrelationID() string { return e.id }

type typedError struct {
	err  error
	kind string
}

func (e typedError) Error() string { return e.err.Error() }

func (e typedError) Unwrap() error { return e.err }

func (e typedError) ErrorType() string { return e.kind }

// Typed declares an error's category at the site that knows it, which is the
// only place that can. Everything downstream reads it back with ErrorType.
func Typed(err error, kind string) error {
	if err == nil || kind == "" {
		return err
	}
	return typedError{err: err, kind: kind}
}

func Correlate(err error) error {
	if err == nil || CorrelationID(err) != "" {
		return err
	}
	return correlatedError{err: err, id: NewCorrelationID()}
}

func CorrelationID(err error) string {
	var correlated interface{ CorrelationID() string }
	if errors.As(err, &correlated) {
		return correlated.CorrelationID()
	}
	return ""
}

func NewCorrelationID() string {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err == nil {
		return "qf_" + hex.EncodeToString(random)
	}
	return fmt.Sprintf("qf_%x", time.Now().UTC().UnixNano())
}

// ErrorType answers which declared category a failure belongs to: a category
// the caller stated, otherwise one of the conditions the standard library
// exposes, otherwise the honest admission that this build cannot classify it.
func ErrorType(err error) string {
	if err == nil {
		return ""
	}
	var typed interface{ ErrorType() string }
	if errors.As(err, &typed) && typed.ErrorType() != "" {
		return typed.ErrorType()
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ErrorCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return ErrorTimeout
	case errors.Is(err, fs.ErrNotExist):
		return ErrorNotFound
	case errors.Is(err, fs.ErrPermission):
		return ErrorPermission
	case errors.Is(err, fs.ErrExist):
		return ErrorAlreadyExists
	}
	return ErrorUnclassified
}
