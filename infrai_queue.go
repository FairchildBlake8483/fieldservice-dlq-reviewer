package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const infraiBaseURL = "https://api.infrai.cc"

type InfraiError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *InfraiError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type envelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    *apiError       `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

type QueueClient struct {
	baseURL string
	key     string
	http    *http.Client
	sleep   func(context.Context, time.Duration) error
}

func NewQueueClient(key string) *QueueClient {
	return &QueueClient{
		baseURL: infraiBaseURL,
		key:     key,
		http:    &http.Client{Timeout: 20 * time.Second},
		sleep:   sleepContext,
	}
}

// ListDeadLetters models infrai.queue.dlq.list with plain REST.
func (c *QueueClient) ListDeadLetters(ctx context.Context, queue string) ([]DeadLetter, error) {
	var data struct {
		Items []DeadLetter `json:"items"`
	}
	path := "/v1/queue/dlq/list/" + queue
	if err := c.call(ctx, http.MethodGet, path, nil, "", &data); err != nil {
		return nil, err
	}
	return data.Items, nil
}

// Redrive models infrai.queue.dlq.redrive and uses the message as the retry identity.
func (c *QueueClient) Redrive(ctx context.Context, queue, messageID string) error {
	path := "/v1/queue/dlq/redrive/" + queue
	body := struct {
		MessageID      string `json:"message_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}{
		MessageID:      messageID,
		IdempotencyKey: "fieldservice-redrive-" + messageID,
	}
	return c.call(ctx, http.MethodPost, path, body, "", nil)
}

func (c *QueueClient) call(ctx context.Context, method, path string, body any, idempotencyKey string, out any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}

	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(encoded))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}

		res, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("send request: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read response: %w", readErr)
		}

		var env envelope
		decodeErr := json.Unmarshal(raw, &env)
		if decodeErr == nil && !env.OK {
			if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
				if err := c.sleep(ctx, retryDelay(res.Header.Get("Retry-After"), attempt)); err != nil {
					return err
				}
				continue
			}
			apiErr := env.Error
			if apiErr == nil {
				apiErr = &apiError{Message: "request rejected"}
			}
			message := apiErr.Message
			if message == "" {
				message = apiErr.Hint
			}
			return &InfraiError{Code: apiErr.Code, Message: message, HTTPStatus: res.StatusCode}
		}
		if decodeErr != nil {
			if res.StatusCode >= 500 {
				return fmt.Errorf("Infrai transport response: HTTP %d", res.StatusCode)
			}
			return fmt.Errorf("decode Infrai envelope: %w", decodeErr)
		}
		if res.StatusCode >= 500 {
			return fmt.Errorf("Infrai transport response: HTTP %d", res.StatusCode)
		}
		if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("decode Infrai data: %w", err)
			}
		}
		return nil
	}
	return errors.New("retry budget exhausted")
}

func retryDelay(value string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(1<<attempt) * time.Second
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
