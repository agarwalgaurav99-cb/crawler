package download

import (
	"time"

	"github.com/cenkalti/backoff/v5"
)

// Helpers expose internals to download_test; compiled only into tests.

func SetRetryBackOff(r *Retry, f func() backoff.BackOff) { r.newBackOff = f }

func SetRetryAfterCap(r *Retry, d time.Duration) { r.retryAfterCap = d }
