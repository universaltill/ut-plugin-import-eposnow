package eposnow

import (
	"errors"
	"io"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// StreamDoer sends a request over an http:stream (http_open), not
// http_request: the till cuts an http_request body off at 256 KiB, and a
// page of 200 products with their nested objects can be larger. The plugin
// bounds a page itself (MaxPageBytes) so memory stays bounded.
type StreamDoer struct{}

// Do opens the stream, reads the status and at most MaxPageBytes+1 bytes of
// body (one past the limit, so the client can tell an oversized page).
func (StreamDoer) Do(r plugin.HTTPRequest) (*plugin.HTTPResponse, error) {
	s, err := plugin.HTTPOpen(r)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	st, err := s.Status()
	if err != nil {
		return nil, err
	}
	body, err := readBody(s)
	if err != nil {
		return nil, err
	}
	return &plugin.HTTPResponse{Status: st.Status, Headers: st.Headers, Body: body}, nil
}

// readBody reads at most MaxPageBytes+1 bytes. The till's http_read answers
// ErrQuota past its body limit: that is ErrPageTooLarge, so the operator
// sees the same "page larger than 8 MiB" error the client gives.
func readBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxPageBytes+1))
	if errors.Is(err, plugin.ErrQuota) {
		return nil, ErrPageTooLarge
	}
	return body, err
}
