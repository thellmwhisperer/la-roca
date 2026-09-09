//go:build !windows

package vector

import (
	"fmt"
	"os"
	"reflect"
)

// File identity and change time survive process restarts and detect replacements
// and timestamp-preserving restores. PRAGMA data_version is connection-local and
// cannot prove a persisted generation. Never include access time: reads change it.
func fileChangeIdentity(_ string, info os.FileInfo) (string, error) {
	stat := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !stat.IsValid() || stat.Kind() != reflect.Struct {
		return "", fmt.Errorf("source file identity is unavailable")
	}
	var fields []any
	for _, name := range []string{"Dev", "Ino", "Ctim", "Ctimespec"} {
		if field := stat.FieldByName(name); field.IsValid() && field.CanInterface() {
			fields = append(fields, field.Interface())
		}
	}
	if len(fields) != 3 {
		return "", fmt.Errorf("source file identity or change time is unavailable")
	}
	return fmt.Sprint(fields), nil
}
