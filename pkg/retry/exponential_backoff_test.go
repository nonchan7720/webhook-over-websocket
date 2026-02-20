package retry

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 再試行回数をカウントするためのヘルパー
type retryCounter struct {
	count      int
	maxFailure int
	err        error
}

func (c *retryCounter) call() (int, error) {
	c.count++
	if c.count <= c.maxFailure {
		return 0, c.err
	}
	return c.count, nil
}

func TestExponentialBackoff_Success(t *testing.T) {
	ctx := t.Context()
	counter := &retryCounter{count: 0, maxFailure: 2, err: errors.New("Temporary error")}

	// 3回目の呼び出しで成功する
	result, err := ExponentialBackoff(ctx, counter.call, WithCalExponentialBackoff(func(retryCount int) time.Duration { return 0 }))

	assert.NoError(t, err, "No errors should occur.")
	assert.Equal(t, 3, result, "It should succeed on the third call.")
	assert.Equal(t, 3, counter.count, "The function should be called three times.")
}

func TestExponentialBackoff_MaxRetryError(t *testing.T) {
	ctx := t.Context()
	counter := &retryCounter{count: 0, maxFailure: 10, err: errors.New("Temporary error")}

	// デフォルトの最大再試行回数（5回）を超えるため、エラーが返される
	result, err := ExponentialBackoff(ctx, counter.call, WithCalExponentialBackoff(func(retryCount int) time.Duration { return 0 }))

	assert.Error(t, err, "An error should occur.")
	assert.Equal(t, ErrMaxRetry, err, "The maximum retry error should be returned.")
	assert.Equal(t, 0, result, "If it fails, the default value should be returned.")
	assert.Equal(t, 5, counter.count, "The function should be called five times.")
}

func TestExponentialBackoff_WithMaxRetries(t *testing.T) {
	ctx := t.Context()
	counter := &retryCounter{count: 0, maxFailure: 2, err: errors.New("Temporary error")}

	// カスタム最大再試行回数（10回）を設定
	result, err := ExponentialBackoff(ctx, counter.call, WithCalExponentialBackoff(func(retryCount int) time.Duration { return 0 }), WithMaxRetries(5))

	assert.NoError(t, err, "No errors should occur.")
	assert.Equal(t, 3, result, "It should succeed on the third call.")
	assert.Equal(t, 3, counter.count, "The function should be called three times.")
}

func TestExponentialBackoff_ImmediateSuccess(t *testing.T) {
	ctx := t.Context()
	counter := &retryCounter{count: 0, maxFailure: 0, err: errors.New("Temporary error")}

	// 初回から成功する
	result, err := ExponentialBackoff(ctx, counter.call, WithCalExponentialBackoff(func(retryCount int) time.Duration { return 0 }))

	assert.NoError(t, err, "No errors should occur.")
	assert.Equal(t, 1, result, "It should succeed on the first call.")
	assert.Equal(t, 1, counter.count, "The function should be called only once.")
}

func TestExponentialBackoff_SkipRetry(t *testing.T) {
	ctx := t.Context()
	skipErr := errors.New("Errors to skip")

	// スキップエラーを返す関数
	fn := func() (int, error) {
		return 0, NewSkip(skipErr)
	}

	result, err := ExponentialBackoff(ctx, fn)

	assert.Error(t, err, "An error should occur.")
	assert.Equal(t, skipErr, err, "The skipped errors should be returned as-is.")
	assert.Equal(t, 0, result, "The default value should be returned.")
}

func TestExponentialBackoffOnlyErr_Success(t *testing.T) {
	ctx := t.Context()
	counter := &retryCounter{count: 0, maxFailure: 2, err: errors.New("Temporary error")}

	err := ExponentialBackoffOnlyErr(ctx, func() error {
		_, err := counter.call()
		return err
	}, WithCalExponentialBackoff(func(retryCount int) time.Duration { return 0 }))

	assert.NoError(t, err, "No errors should occur.")
	assert.Equal(t, 3, counter.count, "The function should be called three times.")
}

func TestExponentialBackoffOnlyErr_Error(t *testing.T) {
	ctx := t.Context()
	counter := &retryCounter{count: 0, maxFailure: 10, err: errors.New("Temporary error")}

	err := ExponentialBackoffOnlyErr(ctx, func() error {
		_, err := counter.call()
		return err
	}, WithCalExponentialBackoff(func(retryCount int) time.Duration { return 0 }))

	assert.Error(t, err, "An error should occur.")
	assert.Equal(t, ErrMaxRetry, err, "The maximum retry error should be returned.")
	assert.Equal(t, 5, counter.count, "The function should be called five times.")
}

func TestExponentialBackoffOnlyErr_WithMaxRetries(t *testing.T) {
	ctx := t.Context()
	counter := &retryCounter{count: 0, maxFailure: 2, err: errors.New("Temporary error")}

	err := ExponentialBackoffOnlyErr(ctx, func() error {
		_, err := counter.call()
		return err
	}, WithMaxRetries(5), WithCalExponentialBackoff(func(retryCount int) time.Duration { return 0 }))

	assert.NoError(t, err, "No errors should occur.")
	assert.Equal(t, 3, counter.count, "The function should be called three times.")
}

func TestWithMaxRetries(t *testing.T) {
	opt := defaultExponentialOption
	WithMaxRetries(10).apply(&opt)
	assert.Equal(t, 10, opt.maxRetries, "The maximum retry count should be set to 10.")
}

func TestSkipError(t *testing.T) {
	originalErr := errors.New("Original Error")
	skipErr := NewSkip(originalErr)

	assert.Error(t, skipErr, "An error should be returned.")
	assert.Equal(t, originalErr.Error(), skipErr.Error(), "The original error message should be retained.")

	var s *skip
	assert.True(t, errors.As(skipErr, &s), "The error type should be correctly parsed.")
	assert.Equal(t, originalErr, s.Err, "An internal error should be retained.")
}
