package download

import (
	"time"

	"github.com/cenkalti/backoff/v5"
)

// The helpers below expose internals to the external download_test package.
// This file is compiled only into test binaries.

func SetRetryBackOff(r *Retry, f func() backoff.BackOff) { r.newBackOff = f }

func RetryBackOff(r *Retry) backoff.BackOff { return r.newBackOff() }

func SetRetryAfterCap(r *Retry, d time.Duration) { r.retryAfterCap = d }
