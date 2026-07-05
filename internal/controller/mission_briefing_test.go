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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	aiv1alpha1 "github.com/dapperdivers/roundtable/api/v1alpha1"
	natspkg "github.com/dapperdivers/roundtable/pkg/nats"
)

// fakeNATSClient is an in-memory natspkg.Client that records publishes and
// fails subjects matched by failSubject.
type fakeNATSClient struct {
	mu          sync.Mutex
	published   map[string][]byte
	failSubject func(subject string) bool
}

func newFakeNATSClient() *fakeNATSClient {
	return &fakeNATSClient{published: map[string][]byte{}}
}

func (f *fakeNATSClient) subjects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.published))
	for s := range f.published {
		out = append(out, s)
	}
	return out
}

func (f *fakeNATSClient) Connect() error    { return nil }
func (f *fakeNATSClient) Close() error      { return nil }
func (f *fakeNATSClient) IsConnected() bool { return true }

func (f *fakeNATSClient) Publish(subject string, data []byte) error {
	if f.failSubject != nil && f.failSubject(subject) {
		return fmt.Errorf("NATS publish to %s failed: nats: no response from stream", subject)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published[subject] = data
	return nil
}

func (f *fakeNATSClient) PublishJSON(subject string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return f.Publish(subject, data)
}

func (f *fakeNATSClient) Subscribe(string, ...natspkg.SubscribeOption) (*nats.Subscription, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeNATSClient) CreateStream(natspkg.StreamConfig) error { return nil }
func (f *fakeNATSClient) DeleteStream(string) error               { return nil }
func (f *fakeNATSClient) StreamInfo(string) (*nats.StreamInfo, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeNATSClient) EnsureConsumer(string, string, natspkg.ConsumerConfig) error { return nil }
func (f *fakeNATSClient) DeleteConsumer(string, string) error                         { return nil }
func (f *fakeNATSClient) PollMessage(string, time.Duration, ...natspkg.SubscribeOption) (*nats.Msg, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeNATSClient) KVPut(string, string, []byte) error { return nil }
func (f *fakeNATSClient) KVGet(string, string) ([]byte, error) {
	return nil, fmt.Errorf("not found")
}
func (f *fakeNATSClient) KVDelete(string, string) error   { return nil }
func (f *fakeNATSClient) KVKeys(string) ([]string, error) { return nil, nil }

var _ = Describe("MissionReconciler.publishBriefing", func() {
	const namespace = "default"
	ctx := context.Background()

	var (
		fake       *fakeNATSClient
		reconciler *MissionReconciler
		cleanup    []func()
	)

	newReconciler := func() *MissionReconciler {
		return &MissionReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(10),
			NATS:     natspkg.NewProviderWithClient(fake, logr.Discard()),
		}
	}

	createKnight := func(name string, subjects []string) {
		knight := &aiv1alpha1.Knight{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: aiv1alpha1.KnightSpec{
				Domain: "operator",
				Model:  "claude-sonnet-4",
				Skills: []string{"general"},
				NATS: aiv1alpha1.KnightNATS{
					URL:           "nats://nats.test:4222",
					Subjects:      subjects,
					Stream:        "rt_dev_tasks",
					ResultsStream: "rt_dev_results",
				},
				Concurrency: 1,
				TaskTimeout: 120,
			},
		}
		Expect(k8sClient.Create(ctx, knight)).To(Succeed())
		cleanup = append(cleanup, func() { _ = k8sClient.Delete(ctx, knight) })
	}

	createRoundTable := func(name, prefix string) {
		rt := &aiv1alpha1.RoundTable{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: aiv1alpha1.RoundTableSpec{
				NATS: aiv1alpha1.RoundTableNATS{
					URL:           "nats://nats.test:4222",
					SubjectPrefix: prefix,
					TasksStream:   "rt_dev_tasks",
					ResultsStream: "rt_dev_results",
				},
			},
		}
		Expect(k8sClient.Create(ctx, rt)).To(Succeed())
		cleanup = append(cleanup, func() { _ = k8sClient.Delete(ctx, rt) })
	}

	newMission := func(name string, knights ...string) *aiv1alpha1.Mission {
		mks := make([]aiv1alpha1.MissionKnight, 0, len(knights))
		for _, k := range knights {
			mks = append(mks, aiv1alpha1.MissionKnight{Name: k})
		}
		return &aiv1alpha1.Mission{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
			Spec: aiv1alpha1.MissionSpec{
				Objective: "test objective",
				Briefing:  "test briefing",
				Knights:   mks,
			},
		}
	}

	BeforeEach(func() {
		fake = newFakeNATSClient()
		reconciler = newReconciler()
		cleanup = nil
	})

	AfterEach(func() {
		for _, fn := range cleanup {
			fn()
		}
	})

	It("publishes only to knight task subjects, never a .briefing broadcast", func() {
		createKnight("brief-knight-a", []string{"rt-dev.tasks.operator.>"})
		mission := newMission("brief-m1", "brief-knight-a")

		Expect(reconciler.publishBriefing(ctx, mission)).To(Succeed())

		subjects := fake.subjects()
		Expect(subjects).To(ConsistOf("rt-dev.tasks.operator.brief-knight-a"))
		for _, s := range subjects {
			Expect(strings.HasSuffix(s, ".briefing")).To(BeFalse(),
				"no stream covers *.briefing — publishing there can never be acked")
		}
	})

	It("falls back to the referenced RoundTable's prefix when knight subjects are unparseable", func() {
		createKnight("brief-knight-b", []string{"malformed-subject"})
		createRoundTable("brief-rt", "rt-dev")
		mission := newMission("brief-m2", "brief-knight-b")
		mission.Spec.RoundTableRef = "brief-rt"

		Expect(reconciler.publishBriefing(ctx, mission)).To(Succeed())
		Expect(fake.subjects()).To(ConsistOf("rt-dev.tasks.operator.brief-knight-b"))
	})

	It("uses a generation-based task ID so retries are idempotent", func() {
		createKnight("brief-knight-c", []string{"rt-dev.tasks.operator.>"})
		mission := newMission("brief-m3", "brief-knight-c")

		Expect(reconciler.publishBriefing(ctx, mission)).To(Succeed())

		var payload natspkg.TaskPayload
		data := fake.published["rt-dev.tasks.operator.brief-knight-c"]
		Expect(json.Unmarshal(data, &payload)).To(Succeed())
		Expect(payload.TaskID).To(Equal("mission-brief-m3-briefing-brief-knight-c-gen1"))
	})

	It("returns an error when no knight received the briefing", func() {
		createKnight("brief-knight-d", []string{"rt-dev.tasks.operator.>"})
		mission := newMission("brief-m4", "brief-knight-d")
		fake.failSubject = func(string) bool { return true }

		err := reconciler.publishBriefing(ctx, mission)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not delivered to any"))
	})

	It("succeeds with a warning when only some knights received the briefing", func() {
		createKnight("brief-knight-e", []string{"rt-dev.tasks.operator.>"})
		createKnight("brief-knight-f", []string{"rt-dev.tasks.operator.>"})
		mission := newMission("brief-m5", "brief-knight-e", "brief-knight-f")
		fake.failSubject = func(subject string) bool {
			return strings.HasSuffix(subject, "brief-knight-f")
		}

		Expect(reconciler.publishBriefing(ctx, mission)).To(Succeed())
		Expect(fake.subjects()).To(ConsistOf("rt-dev.tasks.operator.brief-knight-e"))

		recorder := reconciler.Recorder.(*record.FakeRecorder)
		Eventually(recorder.Events).Should(Receive(ContainSubstring("BriefingPartialDelivery")))
	})

	It("skips ephemeral knights without error", func() {
		mission := newMission("brief-m6")
		mission.Spec.Knights = []aiv1alpha1.MissionKnight{{Name: "ghost", Ephemeral: true}}

		Expect(reconciler.publishBriefing(ctx, mission)).To(Succeed())
		Expect(fake.subjects()).To(BeEmpty())
	})
})
