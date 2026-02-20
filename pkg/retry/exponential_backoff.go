package retry

import (
	"context"
	"math"
	"time"
)

func ExponentialBackoff[T any](ctx context.Context, fn func() (T, error), optionFnc ...RetryOption) (_ T, rErr error) {
	opt := defaultExponentialOption
	for _, fn := range optionFnc {
		fn.apply(&opt)
	}
	return retryFunc(ctx, fn, func(retryCount int) time.Duration { return opt.calculateExponentialBackoff(retryCount) }, optionFnc...)
}

func ExponentialBackoffOnlyErr(ctx context.Context, fn func() error, optionFnc ...RetryOption) (rErr error) {
	_, err := ExponentialBackoff(ctx, func() (any, error) {
		err := fn()
		return nil, err
	}, optionFnc...)
	return err
}

type CalculateExponentialBackoffFn func(retryCount int) time.Duration

type retryOption struct {
	maxRetries                  int
	calculateExponentialBackoff CalculateExponentialBackoffFn
}

var (
	defaultExponentialOption = retryOption{
		maxRetries: 5,
		calculateExponentialBackoff: func(retryCount int) time.Duration {
			return time.Duration(math.Pow(2, float64(retryCount))) * time.Second
		},
	}
)

type RetryOption interface {
	apply(opt *retryOption)
}

type retryOptionFn func(opt *retryOption)

func (fn retryOptionFn) apply(opt *retryOption) {
	fn(opt)
}

func WithMaxRetries(maxRetries int) RetryOption {
	return retryOptionFn(func(opt *retryOption) {
		opt.maxRetries = maxRetries
	})
}

func WithCalExponentialBackoff(fn CalculateExponentialBackoffFn) RetryOption {
	return retryOptionFn(func(opt *retryOption) {
		opt.calculateExponentialBackoff = fn
	})
}
