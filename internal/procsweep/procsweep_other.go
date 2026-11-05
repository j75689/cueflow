//go:build !linux

package procsweep

import "time"

func Find(ids []string) ([]Proc, error) { return nil, ErrUnsupported }

func Stop(ids []string, grace time.Duration) ([]Proc, error) { return nil, ErrUnsupported }
