package web_search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// RequestFailure describes transport failure, never a claim about search coverage.
type RequestFailure struct {
	Code       string
	Attempts   int
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

func (e *RequestFailure) Error() string {
	return fmt.Sprintf("web search %s after %d attempt(s): %v", e.Code, e.Attempts, e.Err)
}
func (e *RequestFailure) Unwrap() error { return e.Err }

func retryDelay(header string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseUint(header, 10, 64); err == nil {
		const maximum = time.Duration(1<<63 - 1)
		if seconds > uint64(maximum/time.Second) {
			return maximum
		}
		return time.Duration(seconds) * time.Second
	} else if number, ok := err.(*strconv.NumError); ok && number.Err == strconv.ErrRange {
		return time.Duration(1<<63 - 1)
	}
	if date, err := http.ParseTime(header); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 200 * time.Millisecond
}

// Both attempts, body reads and backoff share one deadline. The second attempt
// uses the same query/provider/credentials; no tenant policy or filters change.
func boundedSearchRequest(ctx context.Context, client *http.Client, endpoint string, headers http.Header, body []byte, budget time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var failure *RequestFailure
	for attempt := 1; attempt <= 2; attempt++ {
		if ctx.Err() != nil {
			return nil, &RequestFailure{Code: "cancelled_or_timeout", Attempts: attempt - 1, Err: ctx.Err()}
		}
		remaining := time.Until(deadline)
		// Preserve the provider's original response window. Only fast transient
		// failures (or a shorter client timeout) leave budget for another try.
		current, stop := context.WithTimeout(ctx, remaining)
		req, err := http.NewRequestWithContext(current, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			stop()
			return nil, err
		}
		req.Header = headers.Clone()
		response, err := client.Do(req)
		delay := 200 * time.Millisecond
		if err != nil {
			var network net.Error
			timeout := errors.As(err, &network) && network.Timeout()
			failure = &RequestFailure{Code: "transport_error", Attempts: attempt, Retryable: timeout, Err: err}
			if timeout {
				failure.Code = "timeout"
			}
		} else {
			if response.StatusCode == http.StatusOK {
				data, readErr := readCapped(response.Body, 0)
				response.Body.Close()
				stop()
				if readErr == nil {
					return data, nil
				}
				var network net.Error
				timeout := errors.As(readErr, &network) && network.Timeout()
				failure = &RequestFailure{Code: "response_read_error", Attempts: attempt, Retryable: timeout, Err: readErr}
			} else {
				code := response.StatusCode
				failure = &RequestFailure{Code: fmt.Sprintf("http_%d", code), Attempts: attempt,
					Retryable: code == 429 || code == 502 || code == 503 || code == 504, Err: fmt.Errorf("HTTP %d", code)}
				if code == 429 || code == 503 {
					delay = retryDelay(response.Header.Get("Retry-After"), time.Now())
					failure.RetryAfter = delay
				}
				response.Body.Close()
			}
		}
		stop()
		if attempt == 2 || !failure.Retryable || ctx.Err() != nil {
			return nil, failure
		}
		// Do not shorten Retry-After to fit a deadline or start an attempt with no
		// useful response budget. The caller can select another available source.
		if delay >= time.Until(deadline)-100*time.Millisecond {
			return nil, failure
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, &RequestFailure{Code: "cancelled_or_timeout", Attempts: attempt, Err: ctx.Err()}
		case <-timer.C:
		}
	}
	return nil, failure
}
