package retry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

func retryFunc[T any](
	ctx context.Context,
	fn func() (T, error), backoff func(retryCount int) time.Duration, optionFnc ...RetryOption,
) (_ T, rErr error) {
	opt := defaultExponentialOption
	for _, fn := range optionFnc {
		fn.apply(&opt)
	}
	var def T
	for i := range opt.maxRetries {
		select {
		case <-ctx.Done():
			return def, ctx.Err()
		default:
			result, err := fn()
			if err == nil {
				return result, nil
			} else {
				var skipErr *skip
				if errors.As(err, &skipErr) {
					return def, skipErr.Err
				}
			}
			backoff := backoff(i)
			slog.Debug(fmt.Sprintf("Retrying in %v", backoff))
			time.Sleep(backoff)
		}
	}
	return def, ErrMaxRetry
}
