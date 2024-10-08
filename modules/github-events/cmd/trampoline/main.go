/*
Copyright 2024 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chainguard-dev/clog"
	_ "github.com/chainguard-dev/clog/gcp/init"
	"github.com/chainguard-dev/terraform-infra-common/pkg/httpmetrics"
	mce "github.com/chainguard-dev/terraform-infra-common/pkg/httpmetrics/cloudevents"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/google/go-github/v61/github"
	"github.com/sethvargo/go-envconfig"
)

var env = envconfig.MustProcess(context.Background(), &struct {
	Port          int    `env:"PORT, default=8080"`
	IngressURI    string `env:"EVENT_INGRESS_URI, required"`
	WebhookSecret string `env:"WEBHOOK_SECRET, required"`
}{})

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go httpmetrics.ServeMetrics()
	defer httpmetrics.SetupTracer(ctx)()

	ceclient, err := mce.NewClientHTTP("trampoline", mce.WithTarget(ctx, env.IngressURI)...)
	if err != nil {
		clog.FatalContextf(ctx, "failed to create cloudevents client: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		log := clog.FromContext(ctx)

		defer r.Body.Close()

		// https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
		payload, err := github.ValidatePayload(r, []byte(env.WebhookSecret))
		if err != nil {
			log.Errorf("failed to verify webhook: %v", err)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "failed to verify webhook: %v", err)
			return
		}

		// https://docs.github.com/en/webhooks/webhook-events-and-payloads#delivery-headers
		t := github.WebHookType(r)
		if t == "" {
			log.Errorf("missing X-GitHub-Event header")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		t = "dev.chainguard.github." + t
		log = log.With("event-type", t)

		var msg struct {
			Action     string `json:"action"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			log.Warnf("failed to unmarshal payload; action and subject will be unset: %v", err)
		} else {
			log = log.With("action", msg.Action, "repo", msg.Repository.FullName)
		}

		log.Debugf("forwarding event: %s", t)

		event := cloudevents.NewEvent()
		event.SetType(t)
		event.SetSource(r.Host)
		event.SetSubject(msg.Repository.FullName)
		event.SetExtension("action", msg.Action)
		if err := event.SetData(cloudevents.ApplicationJSON, struct {
			When time.Time       `json:"when"`
			Body json.RawMessage `json:"body"`
		}{
			When: time.Now(),
			Body: payload,
		}); err != nil {
			log.Errorf("failed to set data: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		const retryDelay = 10 * time.Millisecond
		const maxRetry = 3
		rctx := cloudevents.ContextWithRetriesExponentialBackoff(context.WithoutCancel(ctx), retryDelay, maxRetry)
		if ceresult := ceclient.Send(rctx, event); cloudevents.IsUndelivered(ceresult) || cloudevents.IsNACK(ceresult) {
			log.Errorf("Failed to deliver event: %v", ceresult)
			w.WriteHeader(http.StatusInternalServerError)
		}
		log.Debugf("event forwarded")
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", env.Port),
		ReadHeaderTimeout: 10 * time.Second,
	}
	clog.FatalContextf(ctx, "ListenAndServe: %v", srv.ListenAndServe())
}
