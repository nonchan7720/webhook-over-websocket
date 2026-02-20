package retry

import (
	"context"
	"time"
)

func Retry[T any](ctx context.Context, fn func() (T, error), optionFnc ...RetryOption) (_ T, rErr error) {
	opt := defaultExponentialOption
	for _, fn := range optionFnc {
		fn.apply(&opt)
	}
	return retryFunc(ctx, fn, func(retryCount int) time.Duration { return 1 * time.Second }, optionFnc...)
}

func RetryOnlyErr(ctx context.Context, fn func() error, optionFnc ...RetryOption) (rErr error) {
	_, err := Retry(ctx, func() (any, error) {
		err := fn()
		return nil, err
	}, optionFnc...)
	return err
}
