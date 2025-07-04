package trampoline

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/types"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v72/github"
	"github.com/jonboulle/clockwork"
)

type fakeClient struct {
	cloudevents.Client

	events []cloudevents.Event
}

func (f *fakeClient) Send(_ context.Context, event cloudevents.Event) cloudevents.Result {
	fmt.Println("send!", event)
	f.events = append(f.events, event)
	return nil
}

func TestTrampoline(t *testing.T) {
	client := &fakeClient{}

	secret := []byte("hunter2")
	clock := clockwork.NewFakeClock()
	opts := ServerOptions{
		Secrets: [][]byte{
			[]byte("badsecret"), // This secret should be ignored
			secret,
		},
	}
	impl := NewServer(client, opts)
	impl.clock = clock

	srv := httptest.NewServer(impl)
	defer srv.Close()

	body := map[string]interface{}{
		"action": "push",
		"repository": map[string]interface{}{
			"full_name": "org/repo",
		},
		"foo": "bar",
	}
	resp, err := sendevent(t, srv.Client(), srv.URL, "push", body, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %v", resp.Status)
	}

	// Generate expected event body
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("error encoding body: %v", err)
	}
	enc, err := json.Marshal(eventData{
		When: clock.Now(),
		Headers: &eventHeaders{
			HookID:     "1234",
			DeliveryID: "5678",
			UserAgent:  t.Name(),
			Event:      "push",
		},
		Body: json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("error encoding body: %v", err)
	}

	want := []cloudevents.Event{{
		Context: cloudevents.EventContextV1{
			Type:            "dev.chainguard.github.push",
			Source:          *types.ParseURIRef("localhost"),
			ID:              "5678",
			DataContentType: cloudevents.StringOfApplicationJSON(),
			Subject:         github.Ptr("org/repo"),
			Extensions: map[string]interface{}{
				"action":     "push",
				"githubhook": "1234",
			},
		}.AsV1(),
		DataEncoded: enc,
	}}
	if diff := cmp.Diff(want, client.events); diff != "" {
		t.Error(diff)
	}
}

func sendevent(t *testing.T, client *http.Client, url string, eventType string, payload interface{}, secret []byte) (*http.Response, error) {
	t.Helper()

	b := new(bytes.Buffer)
	if err := json.NewEncoder(b).Encode(payload); err != nil {
		t.Fatalf("error encoding payload: %v", err)
	}

	// Compute the signature
	mac := hmac.New(sha256.New, secret)
	mac.Write(b.Bytes())
	sig := fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))

	r, err := http.NewRequest(http.MethodPost, url, b)
	if err != nil {
		return nil, err
	}
	r.Host = "localhost"
	r.Header.Add("Content-Type", "application/json")
	r.Header.Add(github.SHA256SignatureHeader, sig)
	r.Header.Add(github.EventTypeHeader, eventType)
	r.Header.Add("X-Github-Hook-ID", "1234")
	r.Header.Add(github.DeliveryIDHeader, "5678")
	r.Header.Set("User-Agent", t.Name())

	return client.Do(r)
}

func TestForbidden(t *testing.T) {
	srv := httptest.NewServer(NewServer(&fakeClient{}, ServerOptions{}))
	defer srv.Close()

	// Doesn't really matter what we send, we just want to ensure we get a forbidden response
	resp, err := sendevent(t, srv.Client(), srv.URL, "push", nil, nil)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unexpected status: %v", resp.Status)
	}
}

func TestWebhookIDFilter(t *testing.T) {
	secret := []byte("hunter2")
	opts := ServerOptions{
		Secrets:   [][]byte{secret},
		WebhookID: []string{"doesnotmatch"},
	}
	srv := httptest.NewServer(NewServer(&fakeClient{}, opts))
	defer srv.Close()

	// Send an event with the requested action
	resp, err := sendevent(t, srv.Client(), srv.URL, "check_run", nil, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("unexpected status: %v", resp.Status)
	}
}

func TestRequestedOnlyWebhook(t *testing.T) {
	secret := []byte("hunter2")
	opts := ServerOptions{
		Secrets:              [][]byte{secret},
		RequestedOnlyWebhook: []string{"1234"},
	}
	srv := httptest.NewServer(NewServer(&fakeClient{}, opts))
	defer srv.Close()

	// Send an event with the requested action
	resp, err := sendevent(t, srv.Client(), srv.URL, "check_run", map[string]interface{}{
		"action": "requested",
		"repository": map[string]interface{}{
			"full_name": "org/repo",
		},
		"organization": map[string]interface{}{
			"login": "org",
		},
	}, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %v", resp.Status)
	}

	// Send the same event again, but without the requested action
	resp, err = sendevent(t, srv.Client(), srv.URL, "check_run", nil, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("unexpected status: %v", resp.Status)
	}
}

func TestExtractPullRequestInfo(t *testing.T) {
	testCases := []struct {
		name      string
		eventType string
		payload   PayloadInfo
		expected  string
	}{
		{
			name:      "pull_request event with valid data",
			eventType: "pull_request",
			payload: PayloadInfo{
				PullRequest: struct {
					Number int  `json:"number,omitempty"`
					Merged bool `json:"merged,omitempty"`
				}{
					Number: 123,
				},
				Repository: struct {
					FullName string `json:"full_name,omitempty"`
					Owner    struct {
						Login string `json:"login,omitempty"`
					} `json:"owner,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					FullName: "foo/bar",
				},
			},
			expected: "foo/bar#123",
		},
		{
			name:      "not a pull_request event",
			eventType: "push",
			payload: PayloadInfo{
				PullRequest: struct {
					Number int  `json:"number,omitempty"`
					Merged bool `json:"merged,omitempty"`
				}{
					Number: 123,
				},
				Repository: struct {
					FullName string `json:"full_name,omitempty"`
					Owner    struct {
						Login string `json:"login,omitempty"`
					} `json:"owner,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					FullName: "foo/bar",
				},
			},
			expected: "",
		},
		{
			name:      "pull_request event with missing number",
			eventType: "pull_request",
			payload: PayloadInfo{
				Repository: struct {
					FullName string `json:"full_name,omitempty"`
					Owner    struct {
						Login string `json:"login,omitempty"`
					} `json:"owner,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					FullName: "foo/bar",
				},
			},
			expected: "",
		},
		{
			name:      "pull_request event with missing repo",
			eventType: "pull_request",
			payload: PayloadInfo{
				PullRequest: struct {
					Number int  `json:"number,omitempty"`
					Merged bool `json:"merged,omitempty"`
				}{
					Number: 123,
				},
			},
			expected: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call the function directly
			result := extractPullRequestInfo(tc.eventType, tc.payload)

			// Check the result
			if result != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestExtractPullRequestURL(t *testing.T) {
	testCases := []struct {
		name      string
		eventType string
		payload   PayloadInfo
		expected  string
	}{
		{
			name:      "pull_request event with valid data",
			eventType: "pull_request",
			payload: PayloadInfo{
				PullRequest: struct {
					Number int  `json:"number,omitempty"`
					Merged bool `json:"merged,omitempty"`
				}{
					Number: 123,
				},
				Repository: struct {
					FullName string `json:"full_name,omitempty"`
					Owner    struct {
						Login string `json:"login,omitempty"`
					} `json:"owner,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					FullName: "foo/bar",
					Owner: struct {
						Login string `json:"login,omitempty"`
					}{
						Login: "foo",
					},
					Name: "bar",
				},
			},
			expected: "https://github.com/foo/bar/pull/123",
		},
		{
			name:      "not a pull_request event",
			eventType: "push",
			payload: PayloadInfo{
				Number: 123,
				Repository: struct {
					FullName string `json:"full_name,omitempty"`
					Owner    struct {
						Login string `json:"login,omitempty"`
					} `json:"owner,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					FullName: "foo/bar",
					Owner: struct {
						Login string `json:"login,omitempty"`
					}{
						Login: "foo",
					},
					Name: "bar",
				},
			},
			expected: "",
		},
		{
			name:      "pull_request event with missing number",
			eventType: "pull_request",
			payload: PayloadInfo{
				Repository: struct {
					FullName string `json:"full_name,omitempty"`
					Owner    struct {
						Login string `json:"login,omitempty"`
					} `json:"owner,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					FullName: "foo/bar",
					Owner: struct {
						Login string `json:"login,omitempty"`
					}{
						Login: "foo",
					},
					Name: "bar",
				},
			},
			expected: "",
		},
		{
			name:      "pull_request event with missing repo",
			eventType: "pull_request",
			payload: PayloadInfo{
				PullRequest: struct {
					Number int  `json:"number,omitempty"`
					Merged bool `json:"merged,omitempty"`
				}{
					Number: 123,
				},
			},
			expected: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call the function directly
			result := extractPullRequestURL(tc.eventType, tc.payload)

			// Check the result
			if result != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestPullRequestExtension(t *testing.T) {
	client := &fakeClient{}
	secret := []byte("hunter2")
	clock := clockwork.NewFakeClock()
	opts := ServerOptions{
		Secrets:   [][]byte{secret},
		WebhookID: []string{"1234"},
	}
	impl := NewServer(client, opts)
	impl.clock = clock

	srv := httptest.NewServer(impl)
	defer srv.Close()

	// Send a pull_request event
	prPayload := map[string]interface{}{
		"action": "opened",
		"pull_request": map[string]interface{}{
			"number": 123,
		},
		"repository": map[string]interface{}{
			"full_name": "foo/bar",
			"owner": map[string]interface{}{
				"login": "foo",
			},
			"name": "bar",
		},
		"organization": map[string]interface{}{
			"login": "foo",
		},
	}

	resp, err := sendevent(t, srv.Client(), srv.URL, "pull_request", prPayload, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %v", resp.Status)
	}

	// Check that the pullrequest extension was added
	if len(client.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(client.events))
	}

	// Check original pullrequest extension
	pullrequest, ok := client.events[0].Extensions()["pullrequest"]
	if !ok {
		t.Logf("Available extensions: %v", client.events[0].Extensions())
		t.Fatal("pullrequest extension not found")
	}
	if pullrequest != "foo/bar#123" {
		t.Errorf("unexpected pullrequest value: %v", pullrequest)
	}

	// Check new pullrequesturl extension
	pullrequesturl, ok := client.events[0].Extensions()["pullrequesturl"]
	if !ok {
		t.Fatal("pullrequesturl extension not found")
	}
	if pullrequesturl != "https://github.com/foo/bar/pull/123" {
		t.Errorf("unexpected pullrequesturl value: %v", pullrequesturl)
	}

	// Reset client events
	client.events = nil

	// Send a non-pull_request event
	nonPrPayload := map[string]interface{}{
		"action": "push",
		"repository": map[string]interface{}{
			"full_name": "foo/bar",
		},
		"organization": map[string]interface{}{
			"login": "foo",
		},
	}

	resp, err = sendevent(t, srv.Client(), srv.URL, "push", nonPrPayload, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %v", resp.Status)
	}

	// Check that no pullrequest extension was added
	if len(client.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(client.events))
	}

	// Check that neither pullrequest extension was added for non-PR events
	_, exists := client.events[0].Extensions()["pullrequest"]
	if exists {
		t.Fatal("pullrequest extension should not be present for non-PR events")
	}

	_, exists = client.events[0].Extensions()["pullrequesturl"]
	if exists {
		t.Fatal("pullrequesturl extension should not be present for non-PR events")
	}
}

func TestExtractIssueURL(t *testing.T) {
	testCases := []struct {
		name      string
		eventType string
		payload   PayloadInfo
		expected  string
	}{
		{
			name:      "issues event with valid data",
			eventType: "issues",
			payload: PayloadInfo{
				Issue: struct {
					Number          int       `json:"number,omitempty"`
					PullRequestInfo *struct{} `json:"pull_request,omitempty"`
				}{
					Number: 456,
				},
				Repository: struct {
					FullName string `json:"full_name,omitempty"`
					Owner    struct {
						Login string `json:"login,omitempty"`
					} `json:"owner,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					FullName: "foo/bar",
					Owner: struct {
						Login string `json:"login,omitempty"`
					}{
						Login: "foo",
					},
					Name: "bar",
				},
			},
			expected: "https://github.com/foo/bar/issues/456",
		},
		{
			name:      "issue_comment on issue (not PR)",
			eventType: "issue_comment",
			payload: PayloadInfo{
				Issue: struct {
					Number          int       `json:"number,omitempty"`
					PullRequestInfo *struct{} `json:"pull_request,omitempty"`
				}{
					Number:          789,
					PullRequestInfo: nil,
				},
				Repository: struct {
					FullName string `json:"full_name,omitempty"`
					Owner    struct {
						Login string `json:"login,omitempty"`
					} `json:"owner,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					FullName: "foo/bar",
					Owner: struct {
						Login string `json:"login,omitempty"`
					}{
						Login: "foo",
					},
					Name: "bar",
				},
			},
			expected: "https://github.com/foo/bar/issues/789",
		},
		{
			name:      "issue_comment on PR (should not return issue URL)",
			eventType: "issue_comment",
			payload: PayloadInfo{
				Issue: struct {
					Number          int       `json:"number,omitempty"`
					PullRequestInfo *struct{} `json:"pull_request,omitempty"`
				}{
					Number:          789,
					PullRequestInfo: &struct{}{},
				},
				Repository: struct {
					FullName string `json:"full_name,omitempty"`
					Owner    struct {
						Login string `json:"login,omitempty"`
					} `json:"owner,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					FullName: "foo/bar",
					Owner: struct {
						Login string `json:"login,omitempty"`
					}{
						Login: "foo",
					},
					Name: "bar",
				},
			},
			expected: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call the function directly
			result := extractIssueURL(tc.eventType, tc.payload)

			// Check the result
			if result != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestIssueURLExtension(t *testing.T) {
	client := &fakeClient{}
	secret := []byte("hunter2")
	clock := clockwork.NewFakeClock()
	opts := ServerOptions{
		Secrets: [][]byte{secret},
	}
	impl := NewServer(client, opts)
	impl.clock = clock

	srv := httptest.NewServer(impl)
	defer srv.Close()

	// Send an issues event
	issuePayload := map[string]interface{}{
		"action": "opened",
		"issue": map[string]interface{}{
			"number": 456,
		},
		"repository": map[string]interface{}{
			"full_name": "foo/bar",
			"owner": map[string]interface{}{
				"login": "foo",
			},
			"name": "bar",
		},
		"organization": map[string]interface{}{
			"login": "foo",
		},
	}

	resp, err := sendevent(t, srv.Client(), srv.URL, "issues", issuePayload, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %v", resp.Status)
	}

	// Check that the issueurl extension was added
	if len(client.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(client.events))
	}

	issueurl, ok := client.events[0].Extensions()["issueurl"]
	if !ok {
		t.Logf("Available extensions: %v", client.events[0].Extensions())
		t.Fatal("issueurl extension not found")
	}
	if issueurl != "https://github.com/foo/bar/issues/456" {
		t.Errorf("unexpected issueurl value: %v", issueurl)
	}

	// Reset client events
	client.events = nil

	// Send an issue_comment event on an issue (not PR)
	issueCommentPayload := map[string]interface{}{
		"action": "created",
		"issue": map[string]interface{}{
			"number": 789,
			// No pull_request field means it's a regular issue
		},
		"comment": map[string]interface{}{
			"id": 123,
		},
		"repository": map[string]interface{}{
			"full_name": "foo/bar",
			"owner": map[string]interface{}{
				"login": "foo",
			},
			"name": "bar",
		},
		"organization": map[string]interface{}{
			"login": "foo",
		},
	}

	resp, err = sendevent(t, srv.Client(), srv.URL, "issue_comment", issueCommentPayload, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %v", resp.Status)
	}

	// Check that the issueurl extension was added
	if len(client.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(client.events))
	}

	issueurl, ok = client.events[0].Extensions()["issueurl"]
	if !ok {
		t.Fatal("issueurl extension not found for issue_comment")
	}
	if issueurl != "https://github.com/foo/bar/issues/789" {
		t.Errorf("unexpected issueurl value: %v", issueurl)
	}

	// Reset client events
	client.events = nil

	// Send an issue_comment event on a PR (should NOT get issueurl)
	prCommentPayload := map[string]interface{}{
		"action": "created",
		"issue": map[string]interface{}{
			"number":       123,
			"pull_request": map[string]interface{}{}, // This indicates it's a PR
		},
		"comment": map[string]interface{}{
			"id": 456,
		},
		"repository": map[string]interface{}{
			"full_name": "foo/bar",
			"owner": map[string]interface{}{
				"login": "foo",
			},
			"name": "bar",
		},
		"organization": map[string]interface{}{
			"login": "foo",
		},
	}

	resp, err = sendevent(t, srv.Client(), srv.URL, "issue_comment", prCommentPayload, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %v", resp.Status)
	}

	// Check that the issueurl extension was NOT added (but pullrequesturl should be)
	if len(client.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(client.events))
	}

	_, hasIssueURL := client.events[0].Extensions()["issueurl"]
	if hasIssueURL {
		t.Fatal("issueurl extension should not be present for PR comments")
	}

	// But it should have pullrequesturl
	pullrequesturl, ok := client.events[0].Extensions()["pullrequesturl"]
	if !ok {
		t.Fatal("pullrequesturl extension not found for PR comment")
	}
	if pullrequesturl != "https://github.com/foo/bar/pull/123" {
		t.Errorf("unexpected pullrequesturl value: %v", pullrequesturl)
	}
}

func TestPullRequestURLExtensionMultipleEventTypes(t *testing.T) {
	client := &fakeClient{}
	secret := []byte("hunter2")
	opts := ServerOptions{
		Secrets: [][]byte{secret},
	}
	impl := NewServer(client, opts)

	srv := httptest.NewServer(impl)
	defer srv.Close()

	testCases := []struct {
		name        string
		eventType   string
		payload     map[string]interface{}
		expectedURL string
	}{
		{
			name:      "pull_request_review event",
			eventType: "pull_request_review",
			payload: map[string]interface{}{
				"action": "submitted",
				"pull_request": map[string]interface{}{
					"number": 456,
				},
				"review": map[string]interface{}{
					"id": 789,
				},
				"repository": map[string]interface{}{
					"full_name": "org/repo",
					"owner": map[string]interface{}{
						"login": "org",
					},
					"name": "repo",
				},
			},
			expectedURL: "https://github.com/org/repo/pull/456",
		},
		{
			name:      "pull_request_review_comment event",
			eventType: "pull_request_review_comment",
			payload: map[string]interface{}{
				"action": "created",
				"pull_request": map[string]interface{}{
					"number": 789,
				},
				"comment": map[string]interface{}{
					"id": 123,
				},
				"repository": map[string]interface{}{
					"full_name": "user/project",
					"owner": map[string]interface{}{
						"login": "user",
					},
					"name": "project",
				},
			},
			expectedURL: "https://github.com/user/project/pull/789",
		},
		{
			name:      "check_run event with PR",
			eventType: "check_run",
			payload: map[string]interface{}{
				"action": "completed",
				"check_run": map[string]interface{}{
					"id": 999,
					"pull_requests": []interface{}{
						map[string]interface{}{
							"number": 111,
						},
					},
				},
				"repository": map[string]interface{}{
					"full_name": "test/repo",
					"owner": map[string]interface{}{
						"login": "test",
					},
					"name": "repo",
				},
			},
			expectedURL: "https://github.com/test/repo/pull/111",
		},
		{
			name:      "check_suite event with PR",
			eventType: "check_suite",
			payload: map[string]interface{}{
				"action": "completed",
				"check_suite": map[string]interface{}{
					"id": 888,
					"pull_requests": []interface{}{
						map[string]interface{}{
							"number": 222,
						},
					},
				},
				"repository": map[string]interface{}{
					"full_name": "corp/app",
					"owner": map[string]interface{}{
						"login": "corp",
					},
					"name": "app",
				},
			},
			expectedURL: "https://github.com/corp/app/pull/222",
		},
		{
			name:      "check_run event without PR",
			eventType: "check_run",
			payload: map[string]interface{}{
				"action": "completed",
				"check_run": map[string]interface{}{
					"id":            777,
					"pull_requests": []interface{}{}, // Empty array
				},
				"repository": map[string]interface{}{
					"full_name": "test/repo",
					"owner": map[string]interface{}{
						"login": "test",
					},
					"name": "repo",
				},
			},
			expectedURL: "", // No URL expected
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset client events
			client.events = nil

			resp, err := sendevent(t, srv.Client(), srv.URL, tc.eventType, tc.payload, secret)
			if err != nil {
				t.Fatalf("error sending event: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("unexpected status: %v", resp.Status)
			}

			// Check the event was received
			if len(client.events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(client.events))
			}

			// Check pullrequesturl extension
			pullrequesturl, hasPRURL := client.events[0].Extensions()["pullrequesturl"]
			if tc.expectedURL != "" {
				if !hasPRURL {
					t.Fatalf("pullrequesturl extension not found for %s event", tc.eventType)
				}
				if pullrequesturl != tc.expectedURL {
					t.Errorf("unexpected pullrequesturl value: got %v, want %v", pullrequesturl, tc.expectedURL)
				}
			} else if hasPRURL {
				t.Errorf("pullrequesturl extension should not be present for %s event without PR", tc.eventType)
			}
		})
	}
}

func TestIsPullRequestMerged(t *testing.T) {
	testCases := []struct {
		name      string
		eventType string
		payload   PayloadInfo
		expected  bool
	}{
		{
			name:      "merged pull request",
			eventType: "pull_request",
			payload: PayloadInfo{
				Action: "closed",
				PullRequest: struct {
					Number int  `json:"number,omitempty"`
					Merged bool `json:"merged,omitempty"`
				}{
					Merged: true,
				},
			},
			expected: true,
		},
		{
			name:      "closed but not merged pull request",
			eventType: "pull_request",
			payload: PayloadInfo{
				Action: "closed",
				PullRequest: struct {
					Number int  `json:"number,omitempty"`
					Merged bool `json:"merged,omitempty"`
				}{
					Merged: false,
				},
			},
			expected: false,
		},
		{
			name:      "open pull request",
			eventType: "pull_request",
			payload: PayloadInfo{
				Action: "opened",
				PullRequest: struct {
					Number int  `json:"number,omitempty"`
					Merged bool `json:"merged,omitempty"`
				}{
					Merged: false,
				},
			},
			expected: false,
		},
		{
			name:      "not a pull request event",
			eventType: "push",
			payload: PayloadInfo{
				Action: "closed",
				PullRequest: struct {
					Number int  `json:"number,omitempty"`
					Merged bool `json:"merged,omitempty"`
				}{
					Merged: true,
				},
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call the function directly
			result := isPullRequestMerged(tc.eventType, tc.payload)

			// Check the result
			if result != tc.expected {
				t.Errorf("Expected %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestOrgFilter(t *testing.T) {
	secret := []byte("hunter2")
	opts := ServerOptions{
		Secrets:   [][]byte{secret},
		OrgFilter: []string{"org"},
	}
	srv := httptest.NewServer(NewServer(&fakeClient{}, opts))
	defer srv.Close()

	// Send an event with the requested action
	resp, err := sendevent(t, srv.Client(), srv.URL, "pull_request", map[string]interface{}{
		"action": "opened",
	}, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("unexpected status: %v", resp.Status)
	}

	resp, err = sendevent(t, srv.Client(), srv.URL, "pull_request", map[string]interface{}{
		"action": "opened",
		"repository": map[string]interface{}{
			"full_name": "org/repo",
		},
		"organization": map[string]interface{}{
			"login": "org",
		},
	}, secret)
	if err != nil {
		t.Fatalf("error sending event: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %v", resp.Status)
	}
}
