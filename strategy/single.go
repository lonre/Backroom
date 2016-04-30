package strategy

import "net/url"

// Single provide proxy from env
type Single struct {
	URL string
}

// ProxyURL return nil proxy url
func (s *Single) ProxyURL() (*url.URL, error) {
	return &url.URL{Host: s.URL}, nil
}

// RetryLimit return max retry times
func (*Single) RetryLimit() int {
	return 3
}

// RetryFallback determine whether fallback to direct when retry failed
func (*Single) RetryFallback() bool {
	return true
}
