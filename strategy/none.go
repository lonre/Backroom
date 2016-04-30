package strategy

import "net/url"

// None provide proxy from env
type None struct {
}

// ProxyURL return nil proxy url
func (*None) ProxyURL() (*url.URL, error) {
	return nil, nil
}

// RetryLimit return max retry times
func (*None) RetryLimit() int {
	return 0 // do not retry
}

// RetryFallback determine whether fallback to direct when retry failed
func (*None) RetryFallback() bool {
	return false
}
