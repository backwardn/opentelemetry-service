// Copyright 2019, OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package jaegerreceiver

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"contrib.go.opencensus.io/exporter/jaeger"
	commonpb "github.com/census-instrumentation/opencensus-proto/gen-go/agent/common/v1"
	tracepb "github.com/census-instrumentation/opencensus-proto/gen-go/trace/v1"
	"github.com/google/go-cmp/cmp"
	model "github.com/jaegertracing/jaeger/model"
	"github.com/jaegertracing/jaeger/proto-gen/api_v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opencensus.io/trace"
	"google.golang.org/grpc"

	"github.com/open-telemetry/opentelemetry-service/consumer/consumerdata"
	"github.com/open-telemetry/opentelemetry-service/exporter/exportertest"
	"github.com/open-telemetry/opentelemetry-service/internal"
	"github.com/open-telemetry/opentelemetry-service/receiver/receivertest"
	tracetranslator "github.com/open-telemetry/opentelemetry-service/translator/trace"
)

func TestReception(t *testing.T) {
	// 1. Create the Jaeger receiver aka "server"
	config := &Configuration{
		CollectorHTTPPort: 14268, // that's the only one used by this test
	}
	sink := new(exportertest.SinkTraceExporter)

	jr, err := New(context.Background(), config, sink)
	defer jr.StopTraceReception()
	assert.NoError(t, err, "should not have failed to create the Jaeger received")

	t.Log("Starting")

	mh := receivertest.NewMockHost()
	err = jr.StartTraceReception(mh)
	assert.NoError(t, err, "should not have failed to start trace reception")

	t.Log("StartTraceReception")

	now := time.Unix(1542158650, 536343000).UTC()
	nowPlus10min := now.Add(10 * time.Minute)
	nowPlus10min2sec := now.Add(10 * time.Minute).Add(2 * time.Second)

	// 2. Then with a "live application", send spans to the Jaeger exporter.
	jexp, err := jaeger.NewExporter(jaeger.Options{
		Process: jaeger.Process{
			ServiceName: "issaTest",
			Tags: []jaeger.Tag{
				jaeger.BoolTag("bool", true),
				jaeger.StringTag("string", "yes"),
				jaeger.Int64Tag("int64", 1e7),
			},
		},
		CollectorEndpoint: fmt.Sprintf("http://localhost:%d/api/traces", config.CollectorHTTPPort),
	})
	assert.NoError(t, err, "should not have failed to create the Jaeger OpenCensus exporter")

	// 3. Now finally send some spans
	for _, sd := range traceFixture(now, nowPlus10min, nowPlus10min2sec) {
		jexp.ExportSpan(sd)
	}
	jexp.Flush()

	got := sink.AllTraces()
	want := expectedTraceData(now, nowPlus10min, nowPlus10min2sec)

	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("Mismatched responses\n-Got +Want:\n\t%s", diff)
	}
}

func TestGRPCReception(t *testing.T) {
	// prepare
	config := &Configuration{
		CollectorGRPCPort: 14250, // that's the only one used by this test
	}
	sink := new(exportertest.SinkTraceExporter)

	jr, err := New(context.Background(), config, sink)
	assert.NoError(t, err, "should not have failed to create a new receiver")
	defer jr.StopTraceReception()

	mh := receivertest.NewMockHost()
	err = jr.StartTraceReception(mh)
	assert.NoError(t, err, "should not have failed to start trace reception")
	t.Log("StartTraceReception")

	conn, err := grpc.Dial(fmt.Sprintf("0.0.0.0:%d", config.CollectorGRPCPort), grpc.WithInsecure())
	require.NoError(t, err)
	defer conn.Close()

	cl := api_v2.NewCollectorServiceClient(conn)

	now := time.Unix(1542158650, 536343000).UTC()
	d10min := 10 * time.Minute
	d2sec := 2 * time.Second
	nowPlus10min := now.Add(d10min)
	nowPlus10min2sec := now.Add(d10min).Add(d2sec)

	// test
	req := grpcFixture(now, d10min, d2sec)
	resp, err := cl.PostSpans(context.Background(), req, grpc.WaitForReady(true))

	// verify
	assert.NoError(t, err, "should not have failed to post spans")
	assert.NotNil(t, resp, "response should not have been nil")

	got := sink.AllTraces()
	want := expectedTraceData(now, nowPlus10min, nowPlus10min2sec)

	assert.Len(t, req.Batch.Spans, len(want[0].Spans), "got a conflicting amount of spans")

	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("Mismatched responses\n-Got +Want:\n\t%s", diff)
	}

}

func expectedTraceData(t1, t2, t3 time.Time) []consumerdata.TraceData {
	traceID := []byte{0xF1, 0xF2, 0xF3, 0xF4, 0xF5, 0xF6, 0xF7, 0xF8, 0xF9, 0xFA, 0xFB, 0xFC, 0xFD, 0xFE, 0xFF, 0x80}
	parentSpanID := []byte{0x1F, 0x1E, 0x1D, 0x1C, 0x1B, 0x1A, 0x19, 0x18}
	childSpanID := []byte{0xAF, 0xAE, 0xAD, 0xAC, 0xAB, 0xAA, 0xA9, 0xA8}

	return []consumerdata.TraceData{
		{
			Node: &commonpb.Node{
				ServiceInfo: &commonpb.ServiceInfo{Name: "issaTest"},
				LibraryInfo: &commonpb.LibraryInfo{},
				Identifier:  &commonpb.ProcessIdentifier{},
				Attributes: map[string]string{
					"bool":   "true",
					"string": "yes",
					"int64":  "10000000",
				},
			},
			Spans: []*tracepb.Span{
				{
					TraceId:      traceID,
					SpanId:       childSpanID,
					ParentSpanId: parentSpanID,
					Name:         &tracepb.TruncatableString{Value: "DBSearch"},
					StartTime:    internal.TimeToTimestamp(t1),
					EndTime:      internal.TimeToTimestamp(t2),
					Status: &tracepb.Status{
						Code:    trace.StatusCodeNotFound,
						Message: "Stale indices",
					},
					Attributes: &tracepb.Span_Attributes{
						AttributeMap: map[string]*tracepb.AttributeValue{
							"error": {
								Value: &tracepb.AttributeValue_BoolValue{BoolValue: true},
							},
						},
					},
					Links: &tracepb.Span_Links{
						Link: []*tracepb.Span_Link{
							{
								TraceId: traceID,
								SpanId:  parentSpanID,
								Type:    tracepb.Span_Link_PARENT_LINKED_SPAN,
							},
						},
					},
				},
				{
					TraceId:   traceID,
					SpanId:    parentSpanID,
					Name:      &tracepb.TruncatableString{Value: "ProxyFetch"},
					StartTime: internal.TimeToTimestamp(t2),
					EndTime:   internal.TimeToTimestamp(t3),
					Status: &tracepb.Status{
						Code:    trace.StatusCodeInternal,
						Message: "Frontend crash",
					},
					Attributes: &tracepb.Span_Attributes{
						AttributeMap: map[string]*tracepb.AttributeValue{
							"error": {
								Value: &tracepb.AttributeValue_BoolValue{BoolValue: true},
							},
						},
					},
				},
			},
			SourceFormat: "jaeger",
		},
	}
}

func traceFixture(t1, t2, t3 time.Time) []*trace.SpanData {
	traceID := trace.TraceID{0xF1, 0xF2, 0xF3, 0xF4, 0xF5, 0xF6, 0xF7, 0xF8, 0xF9, 0xFA, 0xFB, 0xFC, 0xFD, 0xFE, 0xFF, 0x80}
	parentSpanID := trace.SpanID{0x1F, 0x1E, 0x1D, 0x1C, 0x1B, 0x1A, 0x19, 0x18}
	childSpanID := trace.SpanID{0xAF, 0xAE, 0xAD, 0xAC, 0xAB, 0xAA, 0xA9, 0xA8}

	return []*trace.SpanData{
		{
			SpanContext: trace.SpanContext{
				TraceID: traceID,
				SpanID:  childSpanID,
			},
			ParentSpanID: parentSpanID,
			Name:         "DBSearch",
			StartTime:    t1,
			EndTime:      t2,
			Status: trace.Status{
				Code:    trace.StatusCodeNotFound,
				Message: "Stale indices",
			},
			Links: []trace.Link{
				{
					TraceID: traceID,
					SpanID:  parentSpanID,
					Type:    trace.LinkTypeParent,
				},
			},
		},
		{
			SpanContext: trace.SpanContext{
				TraceID: traceID,
				SpanID:  parentSpanID,
			},
			Name:      "ProxyFetch",
			StartTime: t2,
			EndTime:   t3,
			Status: trace.Status{
				Code:    trace.StatusCodeInternal,
				Message: "Frontend crash",
			},
		},
	}
}

func grpcFixture(t1 time.Time, d1, d2 time.Duration) *api_v2.PostSpansRequest {
	traceID := model.TraceID{}
	traceID.Unmarshal([]byte{0xF1, 0xF2, 0xF3, 0xF4, 0xF5, 0xF6, 0xF7, 0xF8, 0xF9, 0xFA, 0xFB, 0xFC, 0xFD, 0xFE, 0xFF, 0x80})
	parentSpanID := model.NewSpanID(binary.BigEndian.Uint64([]byte{0x1F, 0x1E, 0x1D, 0x1C, 0x1B, 0x1A, 0x19, 0x18}))
	childSpanID := model.NewSpanID(binary.BigEndian.Uint64([]byte{0xAF, 0xAE, 0xAD, 0xAC, 0xAB, 0xAA, 0xA9, 0xA8}))

	return &api_v2.PostSpansRequest{
		Batch: model.Batch{
			Process: &model.Process{
				ServiceName: "issaTest",
				Tags: []model.KeyValue{
					model.Bool("bool", true),
					model.String("string", "yes"),
					model.Int64("int64", 1e7),
				},
			},
			Spans: []*model.Span{
				{
					TraceID:       traceID,
					SpanID:        childSpanID,
					OperationName: "DBSearch",
					StartTime:     t1,
					Duration:      d1,
					Tags: []model.KeyValue{
						model.String(tracetranslator.TagStatusMsg, "Stale indices"),
						model.Int64(tracetranslator.TagStatusCode, trace.StatusCodeNotFound),
						model.Bool("error", true),
					},
					References: []model.SpanRef{
						{
							TraceID: traceID,
							SpanID:  parentSpanID,
							RefType: model.SpanRefType_CHILD_OF,
						},
					},
				},
				{
					TraceID:       traceID,
					SpanID:        parentSpanID,
					OperationName: "ProxyFetch",
					StartTime:     t1.Add(d1),
					Duration:      d2,
					Tags: []model.KeyValue{
						model.String(tracetranslator.TagStatusMsg, "Frontend crash"),
						model.Int64(tracetranslator.TagStatusCode, trace.StatusCodeInternal),
						model.Bool("error", true),
					},
				},
			},
		},
	}
}
