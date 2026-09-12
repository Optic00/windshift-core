package netbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"windshift/internal/utils"
)

const maxResponseBody = 1 << 20

type safeTransport struct {
	base   *url.URL
	client *http.Client
}

func NewSafeTransport(baseURL string) Transport {
	normalized, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return TransportFunc(func(context.Context, string, string, []byte, map[string]string) (*Response, error) {
			return nil, invalid("invalid NetBox base URL")
		})
	}
	base, _ := url.Parse(normalized)
	httpTransport := utils.ConfigureHTTPTransport(&http.Transport{
		DialContext:       utils.SafeNetDialer(10 * time.Second).DialContext,
		Proxy:             nil,
		DisableKeepAlives: true, // Clients are operation-scoped, not a shared pool.
	})
	return &safeTransport{base: base, client: &http.Client{
		Timeout:   30 * time.Second,
		Transport: httpTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

var allowedAPIPath = regexp.MustCompile(`^/api/(dcim/devices|virtualization/virtual-machines)/(?:[1-9][0-9]*/)?$`)

func (t *safeTransport) Do(ctx context.Context, method, targetURL string, body []byte, headers map[string]string) (*Response, error) {
	if method != http.MethodGet || len(body) != 0 {
		return nil, errors.New("NetBox transport permits GET requests only")
	}
	target, err := url.Parse(targetURL)
	if err != nil || target.Scheme != t.base.Scheme || target.Host != t.base.Host || target.User != nil || target.Fragment != "" {
		return nil, errors.New("invalid NetBox request target")
	}
	prefix := strings.TrimRight(t.base.EscapedPath(), "/")
	path := target.EscapedPath()
	if !strings.HasPrefix(path, prefix) || !allowedAPIPath.MatchString(strings.TrimPrefix(path, prefix)) {
		return nil, errors.New("invalid NetBox API path")
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), http.NoBody)
	if err != nil {
		return nil, errors.New("invalid NetBox request")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := t.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	limited := io.LimitReader(response.Body, maxResponseBody+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read NetBox response: %w", err)
	}
	if len(responseBody) > maxResponseBody {
		return nil, errors.New("NetBox response body too large")
	}
	return &Response{StatusCode: response.StatusCode, Body: responseBody}, nil
}
