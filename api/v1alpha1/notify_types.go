/*
Copyright 2026 dapperdivers.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

// NotifySpec configures a completion notification fired exactly once per run
// when the resource enters a terminal phase.
type NotifySpec struct {
	// webhook posts a roundtable.notify/v1 JSON payload to an HTTP endpoint.
	// The URL must match one of the operator's allowed URL prefixes
	// (notify.allowedURLPrefixes Helm value); non-matching URLs are rejected
	// with a NotificationSent=False condition and a warning Event.
	// +optional
	Webhook *WebhookSink `json:"webhook,omitempty"`
}

// WebhookSink describes an HTTP endpoint to notify on completion.
type WebhookSink struct {
	// url is the endpoint to POST the completion payload to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^https?://`
	URL string `json:"url"`

	// tokenSecretRef references a Secret key (in the resource's namespace)
	// holding a bearer token sent in the Authorization header. Never inline
	// tokens in the spec.
	// +optional
	TokenSecretRef *corev1.SecretKeySelector `json:"tokenSecretRef,omitempty"`

	// context is an opaque map echoed verbatim in the payload, letting the
	// caller correlate the completion back to its origin (e.g. a chat
	// session or channel).
	// +optional
	Context map[string]string `json:"context,omitempty"`
}
