package retry

import "errors"

var (
	ErrMaxRetry = errors.New("failed max retries")
)

type skip struct {
	Err error
}

var (
	_ error = (*skip)(nil) //nolint: errcheck
)

func (s *skip) Error() string {
	return s.Err.Error()
}

func NewSkip(err error) error {
	return &skip{Err: err}
}
