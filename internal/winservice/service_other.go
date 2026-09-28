//go:build !windows

package winservice

import (
	"context"
	"errors"
)

func Run(name string, run func(context.Context) error) error {
	return errors.New("Windows Service mode is only available on Windows; use run on this platform")
}
