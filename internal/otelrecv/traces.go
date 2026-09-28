package otelrecv

import (
	"context"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TracesReceiver explicitly rejects traces until the service can persist them.
type TracesReceiver struct {
	coltracepb.UnimplementedTraceServiceServer
}

func NewTracesReceiver() *TracesReceiver {
	return &TracesReceiver{}
}

func (r *TracesReceiver) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	return nil, status.Error(codes.Unimplemented, "trace ingestion is not supported")
}
