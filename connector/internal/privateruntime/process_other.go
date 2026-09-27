//go:build !darwin && !linux

package privateruntime

import "context"

func lock(string) (func(), error)                      { return nil, ErrClosed }
func runProcess(context.Context, string, string) error { return ErrClosed }
