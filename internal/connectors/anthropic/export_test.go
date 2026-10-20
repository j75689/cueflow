package anthropic

import "time"

// SetWindowTimeout shortens the Models API lookup's timeout for a test and returns a restore func.
func SetWindowTimeout(d time.Duration) func() {
	old := windowTimeout
	windowTimeout = d
	return func() { windowTimeout = old }
}
