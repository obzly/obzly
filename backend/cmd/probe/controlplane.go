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

	"obzly/internal/synthetics/spec"
)

// errRevoked: the control plane refused the token, so this probe has
// nothing it may run.
var errRevoked = errors.New("the control plane refused this probe's token")

type controlPlane struct {
	base     string
	token    string
	http     *http.Client
	instance string
	// onMembers receives the group's live members from every answer;
	// ownedChecks is how many monitors this instance runs now.
	onMembers   func([]string)
	ownedChecks func() int
}

// config fetches the probe's config; nil with no error means unchanged.
func (c *controlPlane) config(ctx context.Context, etag string) (*spec.ProbeConfig, string, error) {
	request, err := c.request(ctx, http.MethodGet, "/api/v1/synthetics/probe/config", nil)
	if err != nil {
		return nil, "", err
	}
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = response.Body.Close() }()
	c.readMembers(response)
	switch response.StatusCode {
	case http.StatusNotModified:
		return nil, etag, nil
	case http.StatusOK:
		var config spec.ProbeConfig
		if err := json.NewDecoder(response.Body).Decode(&config); err != nil {
			return nil, "", fmt.Errorf("invalid config: %w", err)
		}
		return &config, response.Header.Get("ETag"), nil
	case http.StatusUnauthorized:
		return nil, "", errRevoked
	default:
		return nil, "", unexpected(response)
	}
}

// nextTask long-polls for an interactive test; nil when none arrived.
func (c *controlPlane) nextTask(ctx context.Context, wait time.Duration) (*spec.Task, error) {
	path := fmt.Sprintf("/api/v1/synthetics/probe/tasks?wait=%d", int(wait.Seconds()))
	request, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	client := *c.http
	client.Timeout = wait + 20*time.Second
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	c.readMembers(response)
	switch response.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var task spec.Task
		if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
			return nil, fmt.Errorf("invalid task: %w", err)
		}
		return &task, nil
	case http.StatusUnauthorized:
		return nil, errRevoked
	default:
		return nil, unexpected(response)
	}
}

func (c *controlPlane) postResult(ctx context.Context, taskID string, result spec.RunResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	request, err := c.request(
		ctx,
		http.MethodPost,
		"/api/v1/synthetics/probe/tasks/"+taskID+"/result",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return unexpected(response)
	}
	return nil
}

// postJSON posts body to path and decodes a 200 answer into target (nil
// to ignore it).
func (c *controlPlane) postJSON(ctx context.Context, path string, body, target any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := c.request(ctx, http.MethodPost, path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return unexpected(response)
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func (c *controlPlane) postCloudResult(ctx context.Context, taskID string, scan spec.CloudScan) error {
	return c.postJSON(ctx, "/api/v1/synthetics/probe/tasks/"+taskID+"/cloud-result", scan, nil)
}

func (c *controlPlane) reportCloudScan(ctx context.Context, connectionID string, scan spec.CloudScan) error {
	return c.postJSON(ctx, "/api/v1/synthetics/probe/cloud/"+connectionID+"/scan", scan, nil)
}

// leave tells the control plane this instance stops, so its monitors move to the other members
// at once.
func (c *controlPlane) leave(ctx context.Context) error {
	return c.postJSON(ctx, "/api/v1/synthetics/probe/leave", struct{}{}, nil)
}

// cloudToken is the control plane's federated token for a connection.
func (c *controlPlane) cloudToken(ctx context.Context, connectionID string) (spec.CloudToken, error) {
	var token spec.CloudToken
	err := c.postJSON(ctx, "/api/v1/synthetics/probe/cloud/"+connectionID+"/token", struct{}{}, &token)
	return token, err
}

// taskCloudToken is the federated token for a Test access task (its
// connection isn't saved yet).
func (c *controlPlane) taskCloudToken(ctx context.Context, taskID string) (spec.CloudToken, error) {
	var token spec.CloudToken
	err := c.postJSON(ctx, "/api/v1/synthetics/probe/tasks/"+taskID+"/cloud-token", struct{}{}, &token)
	return token, err
}

func (c *controlPlane) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-Probe-Version", version)
	request.Header.Set("User-Agent", "obzly-probe/"+version)
	if c.instance != "" {
		request.Header.Set(spec.HeaderInstance, c.instance)
	}
	if c.ownedChecks != nil {
		request.Header.Set(spec.HeaderChecks, strconv.Itoa(c.ownedChecks()))
	}
	return request, nil
}

// readMembers passes on the group's live members, when the answer has them
// (a control plane from before groups sends none).
func (c *controlPlane) readMembers(response *http.Response) {
	if c.onMembers == nil || response.StatusCode == http.StatusUnauthorized {
		return
	}
	if values, ok := response.Header[http.CanonicalHeaderKey(spec.HeaderMembers)]; ok && len(values) > 0 {
		var members []string
		for _, name := range strings.Split(values[0], ",") {
			if name = strings.TrimSpace(name); name != "" {
				members = append(members, name)
			}
		}
		c.onMembers(members)
	}
}

func unexpected(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 300))
	return fmt.Errorf("control plane answered %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
}
