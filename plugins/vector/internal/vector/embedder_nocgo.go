//go:build !cgo && !windows

package vector

import (
	"context"
	"fmt"
)

func (n *Native) Embed(context.Context, string, []string) ([][]float32, error) {
	return nil, fmt.Errorf("this build does not include the local embedding engine")
}

func (n *Native) Close() {}

func (n *Native) Prewarm(context.Context) error {
	return fmt.Errorf("this build does not include the local embedding engine")
}

func readMem(info *int64) { *info = 0 }
