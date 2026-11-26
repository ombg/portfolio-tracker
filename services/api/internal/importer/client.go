// Package importer calls the importer service's internal API (specs/architecture.md, "importer internal API").
// Every call carries the shared service token and the signed-in user's id; the importer applies RLS with it (D-35).
package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrUnavailable means the importer could not be reached or failed; the API answers 502.
var ErrUnavailable = errors.New("importer unavailable")

// maxResponse bounds what the API reads back; importer answers are small JSON objects.
const maxResponse = 1 << 20

type Client struct {
	BaseURL string // e.g. http://importer:8000
	Token   string // SERVICE_TOKEN
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		// Parsing a large export takes a few seconds; the upload itself streams through.
		HTTP: &http.Client{Timeout: 5 * time.Minute},
	}
}

// Response is the importer's answer to a request it handled: its status and JSON body, which already has the
// {"error", "message"} shape for 4xx.
type Response struct {
	Status int
	Body   []byte
}

// Upload streams a multipart body (fields broker and account_id, part file) to POST /internal/imports.
func (c *Client) Upload(ctx context.Context, userID uuid.UUID, contentType string, body io.Reader) (Response, error) {
	return c.do(ctx, http.MethodPost, "/internal/imports", userID, contentType, body)
}

// Reparse asks the importer to parse a batch's stored original again (POST /internal/imports/{id}/reparse).
func (c *Client) Reparse(ctx context.Context, userID, batchID uuid.UUID) (Response, error) {
	return c.do(ctx, http.MethodPost, "/internal/imports/"+batchID.String()+"/reparse", userID, "", nil)
}

// EditRow sends a row correction, a JSON object with type, security and/or excluded (D-36).
func (c *Client) EditRow(ctx context.Context, userID, batchID uuid.UUID, rowNo int32, body []byte) (Response, error) {
	path := fmt.Sprintf("/internal/imports/%s/rows/%d", batchID, rowNo)
	return c.do(ctx, http.MethodPatch, path, userID, "application/json", bytes.NewReader(body))
}

func (c *Client) do(ctx context.Context, method, path string, userID uuid.UUID, contentType string, body io.Reader) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("X-Service-Token", c.Token)
	req.Header.Set("X-User-Id", userID.String())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return Response{}, fmt.Errorf("%w: read response: %v", ErrUnavailable, err)
	}
	// A 5xx is the importer's own failure; its details stay in its log, not in the browser.
	if res.StatusCode >= 500 {
		return Response{}, fmt.Errorf("%w: status %d", ErrUnavailable, res.StatusCode)
	}
	return Response{Status: res.StatusCode, Body: b}, nil
}
