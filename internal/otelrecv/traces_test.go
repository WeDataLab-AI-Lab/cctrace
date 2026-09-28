package otelrecv

import (
	"context"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTracesReceiverRejectsUnsupportedIngestion(t *testing.T) {
	_, err := NewTracesReceiver().Export(context.Background(), &coltracepb.ExportTraceServiceRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("status = %v, want Unimplemented", status.Code(err))
	}
}
