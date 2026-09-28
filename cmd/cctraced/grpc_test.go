package main

import (
	"context"
	"net"
	"testing"
	"time"

	"cctrace/internal/auth"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type recordingLogsServer struct {
	collogspb.UnimplementedLogsServiceServer
	requests chan *collogspb.ExportLogsServiceRequest
}

func (s *recordingLogsServer) Export(_ context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	s.requests <- req
	return &collogspb.ExportLogsServiceResponse{}, nil
}

func TestGRPCLogsExportAuthentication(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logs := &recordingLogsServer{requests: make(chan *collogspb.ExportLogsServiceRequest, 2)}
	server := newGRPCServer(auth.New("test-key"), logs,
		&colmetricspb.UnimplementedMetricsServiceServer{}, &coltracepb.UnimplementedTraceServiceServer{})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := collogspb.NewLogsServiceClient(conn)
	request := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{SchemaUrl: "test-schema"}}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authorized := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer test-key")
	if _, err := client.Export(authorized, request); err != nil {
		t.Fatalf("authorized Export: %v", err)
	}
	select {
	case got := <-logs.requests:
		if !proto.Equal(got, request) {
			t.Fatalf("received %v, want %v", got, request)
		}
	default:
		t.Fatal("authorized Export did not reach receiver")
	}
	if _, err := client.Export(ctx, request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated Export: got %v, want Unauthenticated", err)
	}
	select {
	case <-logs.requests:
		t.Fatal("unauthenticated Export reached receiver")
	default:
	}
}
