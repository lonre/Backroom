package strategy

import (
	"net/url"

	"gopkg.in/redis.v3"

	log "github.com/Sirupsen/logrus"
)

// Redis provide proxy from redis
type Redis struct {
	URL    string
	client *redis.Client
}

// ProxyURL return proxy by redis
func (r *Redis) ProxyURL() (*url.URL, error) {
	result, err := r.client.RandomKey().Result()
	if err != nil {
		log.Debugf("strategy: redis: get ProxyURL error: %s", err.Error())
		return nil, err
	}
	return &url.URL{Host: result}, nil
}

// RetryLimit return max retry times
func (*Redis) RetryLimit() int {
	return 3
}

// RetryFallback determine whether fallback to direct when retry failed
func (*Redis) RetryFallback() bool {
	return true
}

// NewRedis return new redis strategy
func NewRedis(url string) *Redis {
	r := &Redis{URL: url}
	r.client = redis.NewClient(&redis.Options{
		Addr:       url,
		DB:         6,
		MaxRetries: 1,
	})
	return r
}
