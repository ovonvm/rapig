package winapp

import (
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 3 * time.Second}

func newRequest(method, url string) (*http.Request, error) {
	return http.NewRequest(method, url, nil)
}
