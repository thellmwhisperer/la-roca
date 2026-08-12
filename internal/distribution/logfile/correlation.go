package logfile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"time"
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

func Correlate(err error) error {
	if err == nil || CorrelationID(err) != "" {
		return err
	}
	return correlatedError{err: err, id: NewCorrelationID()}
}

func CorrelationID(err error) string {
	for err != nil {
		if correlated, ok := err.(interface{ CorrelationID() string }); ok {
			return correlated.CorrelationID()
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return ""
		}
		err = unwrapped.Unwrap()
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

func ErrorType(err error) string {
	for err != nil {
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok || unwrapped.Unwrap() == nil {
			break
		}
		err = unwrapped.Unwrap()
	}
	if err == nil {
		return ""
	}
	return reflect.TypeOf(err).String()
}
