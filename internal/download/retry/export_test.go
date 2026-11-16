package retry

import (
	"time"

	"github.com/cenkalti/backoff/v5"
)

// SetRetryBackOff and SetRetryAfterCap expose internals to retry_test.
//
//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -destination=../../../mocks/backoff/backoff.go -package=mocks github.com/cenkalti/backoff/v5 BackOff

func SetRetryBackOff(r *Retry, f func() backoff.BackOff) { r.newBackOff = f }

func SetRetryAfterCap(r *Retry, d time.Duration) { r.retryAfterCap = d }
