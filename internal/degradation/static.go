package degradation

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

// StaticResponseStrategy 静态响应降级策略
type StaticResponseStrategy struct {
	statusCode int
	headers    map[string]string
	body       []byte
}

func NewStaticResponseStrategy(statusCode int, headers map[string]string, body string) *StaticResponseStrategy {
	return &StaticResponseStrategy{
		statusCode: statusCode,
		headers:    headers,
		body:       []byte(body),
	}
}

func (s *StaticResponseStrategy) Name() string { return "static_response" }

func (s *StaticResponseStrategy) Execute(ctx context.Context, req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	for k, v := range s.headers {
		header.Set(k, v)
	}

	return &http.Response{
		StatusCode: s.statusCode,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(s.body)),
	}, nil
}
