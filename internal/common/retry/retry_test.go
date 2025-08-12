package retry

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- Exponential ---

func TestExponential_SuccessImmediate(t *testing.T) {
	err := Exponential(func() error { return nil }, ExponentialConfig{
		InitialInterval: 5 * time.Millisecond,
		MaxElapsedTime:  100 * time.Millisecond,
	})
	assert.NoError(t, err)
}

func TestExponential_RetryThenSuccess(t *testing.T) {
	var calls int
	var onRetryCount int

	err := Exponential(func() error {
		if calls < 3 {
			calls++
			return errors.New("temporary error")
		}
		return nil
	}, ExponentialConfig{
		InitialInterval: 2 * time.Millisecond,
		MaxElapsedTime:  200 * time.Millisecond,
		OnRetry: func(err error, next time.Duration) {
			onRetryCount++
			assert.Error(t, err)
			assert.Greater(t, next, time.Duration(0))
		},
	})

	assert.NoError(t, err)
	assert.Equal(t, 3, calls, "should retry exactly 3 times before success")
	// OnRetry is called for each failed attempt
	assert.Equal(t, 3, onRetryCount)
}

func TestExponential_InvalidConfig(t *testing.T) {
	err := Exponential(func() error { return nil }, ExponentialConfig{
		InitialInterval: 0, // invalid
	})
	assert.Error(t, err)
}

func TestExponential_ExhaustedByTime(t *testing.T) {
	err := Exponential(func() error { return errors.New("always fail") }, ExponentialConfig{
		InitialInterval: 5 * time.Millisecond,
		MaxElapsedTime:  15 * time.Millisecond, // rất ngắn để chắc chắn bị timeout
	})
	assert.Error(t, err, "should fail when MaxElapsedTime is exceeded")
}

// --- Constant ---

func TestConstant_SuccessImmediate(t *testing.T) {
	err := Constant(func() error { return nil }, 10*time.Millisecond, 3)
	assert.NoError(t, err)
}

func TestConstant_RetryExactlyNThenFail(t *testing.T) {
	attempts := 3
	var calls int
	err := Constant(func() error {
		calls++
		return errors.New("fail")
	}, 5*time.Millisecond, attempts)

	assert.Error(t, err)
	assert.Equal(t, attempts, calls, "must call exactly 'attempts' times")
}

func TestConstant_RetryThenSuccessBeforeMax(t *testing.T) {
	attempts := 5
	var calls int
	err := Constant(func() error {
		if calls < 2 {
			calls++
			return errors.New("temporary")
		}
		return nil
	}, 2*time.Millisecond, attempts)

	assert.NoError(t, err)
	assert.Equal(t, 2, calls, "should fail twice then succeed")
}

func TestConstant_AttemptsNonPositiveMeansOneAttempt(t *testing.T) {
	var calls int
	err := Constant(func() error {
		calls++
		return errors.New("fail once")
	}, 1*time.Millisecond, 0) // <=0 => 1 attempt

	assert.Error(t, err)
	assert.Equal(t, 1, calls, "should only attempt once when attempts<=0")
}
