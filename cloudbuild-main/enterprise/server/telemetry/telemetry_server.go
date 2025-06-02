package telemetry

import (
	"context"
	"flag"
	"fmt"
	"net"

	"github.com/ninja-cloudbuild/cloudbuild/server/environment"
	"github.com/ninja-cloudbuild/cloudbuild/server/interfaces"
	"github.com/ninja-cloudbuild/cloudbuild/server/rpc/interceptors"
	"github.com/ninja-cloudbuild/cloudbuild/server/tables"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/db"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	telpb "github.com/ninja-cloudbuild/cloudbuild/proto/telemetry"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
)

var (
	telemetryPort = flag.Int("telemetry_port", 9099, "The port on which to listen for telemetry events")
	_             = flag.Bool("verbose_telemetry_server", false, "If true; print telemetry server information")
)

type TelemetryServer struct {
	env environment.Env
	h   interfaces.DBHandle
}

func NewTelemetryServer(env environment.Env, h interfaces.DBHandle) *TelemetryServer {
	return &TelemetryServer{
		env: env,
		h:   h,
	}
}

func (t *TelemetryServer) StartOrDieIfEnabled() {
	if *telemetryPort < 0 {
		log.Debug("Telemetry collection disabled")
		return
	}
	log.Debug("Telemetry collection enabled")

	grpcOptions := []grpc.ServerOption{
		interceptors.GetUnaryInterceptor(t.env),
		grpc.UnaryInterceptor(grpc_prometheus.UnaryServerInterceptor),
	}

	grpcServer := grpc.NewServer(grpcOptions...)
	telpb.RegisterTelemetryServer(grpcServer, t)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *telemetryPort))
	if err != nil {
		log.Fatalf("Failed to listen on telemetry port: %s", err)
	}
	go func() {
		err := grpcServer.Serve(lis)
		log.Fatal(err.Error())
	}()
}

func (t *TelemetryServer) LogTelemetry(ctx context.Context, req *telpb.LogTelemetryRequest) (*telpb.LogTelemetryResponse, error) {
	log.Debugf("Telemetry data received: %+v", req)

	status := &statuspb.Status{
		Code:    int32(codes.OK),
		Message: "Success",
	}
	for _, logProto := range req.Log {
		log := recordFromLogProto(logProto)
		if err := t.insertLogIfNotExists(ctx, log); err != nil {
			status = &statuspb.Status{
				Code:    int32(codes.DataLoss),
				Message: err.Error(),
			}
		}
	}

	return &telpb.LogTelemetryResponse{
		Status: status,
	}, nil
}

func (t *TelemetryServer) insertLogIfNotExists(ctx context.Context, telemetryLog *tables.TelemetryLog) error {
	return t.h.Transaction(ctx, func(tx *db.DB) error {
		var existing tables.TelemetryLog
		err := tx.Where("installation_uuid = ? AND instance_uuid = ? AND telemetry_log_uuid = ?",
			telemetryLog.InstallationUUID, telemetryLog.InstanceUUID, telemetryLog.TelemetryLogUUID).First(&existing).Error
		if err == nil {
			return nil
		}
		if db.IsRecordNotFound(err) {
			return tx.Create(telemetryLog).Error
		}
		return err
	})
}

func recordFromLogProto(logProto *telpb.TelemetryLog) *tables.TelemetryLog {
	telemetryLog := &tables.TelemetryLog{
		InstallationUUID: logProto.InstallationUuid,
		InstanceUUID:     logProto.InstanceUuid,
		TelemetryLogUUID: logProto.LogUuid,
		RecordedAtUsec:   logProto.RecordedAtUsec,
		AppVersion:       logProto.AppVersion,
		AppURL:           logProto.AppUrl,
		Hostname:         logProto.Hostname,
	}

	if logProto.TelemetryStat != nil {
		telemetryLog.InvocationCount = logProto.TelemetryStat.InvocationCount
		telemetryLog.RegisteredUserCount = logProto.TelemetryStat.RegisteredUserCount
		telemetryLog.BazelUserCount = logProto.TelemetryStat.BazelUserCount
		telemetryLog.BazelHostCount = logProto.TelemetryStat.BazelHostCount
	}

	if logProto.TelemetryFeature != nil {
		telemetryLog.FeatureCacheEnabled = logProto.TelemetryFeature.CacheEnabled
		telemetryLog.FeatureRBEEnabled = logProto.TelemetryFeature.RbeEnabled
		telemetryLog.FeatureAPIEnabled = logProto.TelemetryFeature.ApiEnabled
		telemetryLog.FeatureAuthEnabled = logProto.TelemetryFeature.AuthEnabled
	}

	return telemetryLog
}
