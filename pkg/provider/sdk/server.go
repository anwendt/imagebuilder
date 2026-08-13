package sdk

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"

	providerv1 "github.com/anwendt/imagebuilder/api/provider/v1"
	providererrors "github.com/anwendt/imagebuilder/pkg/provider/errors"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

type Server struct {
	providerv1.UnimplementedPlatformProviderServer
	provider Provider
}

func NewServer(provider Provider) (*Server, error) {
	if provider == nil {
		return nil, fmt.Errorf("provider is required")
	}
	return &Server{provider: provider}, nil
}

func (s *Server) Register(registrar grpc.ServiceRegistrar) {
	providerv1.RegisterPlatformProviderServer(registrar, s)
}

func Serve(ctx context.Context, listener net.Listener, provider Provider, opts ...grpc.ServerOption) error {
	server, err := NewServer(provider)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(opts...)
	server.Register(grpcServer)
	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()
	if err := grpcServer.Serve(listener); err != nil && ctx.Err() == nil {
		return fmt.Errorf("serve provider grpc: %w", err)
	}
	return nil
}

func ServerOptionsFromEnv() ([]grpc.ServerOption, error) {
	mode := os.Getenv("PROVIDER_GRPC_TLS_MODE")
	if mode == "" || mode == "Disabled" {
		return nil, nil
	}
	if mode != "Mutual" {
		return nil, fmt.Errorf("unsupported PROVIDER_GRPC_TLS_MODE %q", mode)
	}
	certFile := os.Getenv("PROVIDER_GRPC_TLS_CERT_FILE")
	keyFile := os.Getenv("PROVIDER_GRPC_TLS_KEY_FILE")
	clientCAFile := os.Getenv("PROVIDER_GRPC_TLS_CLIENT_CA_FILE")
	if certFile == "" || keyFile == "" || clientCAFile == "" {
		return nil, fmt.Errorf("PROVIDER_GRPC_TLS_CERT_FILE, PROVIDER_GRPC_TLS_KEY_FILE, and PROVIDER_GRPC_TLS_CLIENT_CA_FILE are required for mTLS")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load provider server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(clientCAFile) // #nosec G304 G703 -- Path is supplied by trusted provider deployment environment.
	if err != nil {
		return nil, fmt.Errorf("read provider client CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("provider client CA contains no certificates")
	}
	return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		Certificates: []tls.Certificate{cert},
	}))}, nil
}

func (s *Server) GetCapabilities(ctx context.Context, _ *providerv1.Empty) (*providerv1.CapabilitiesResponse, error) {
	caps, err := s.provider.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if caps.ProviderName == "" {
		return nil, fmt.Errorf("provider capabilities must include provider name")
	}
	if caps.ProviderVersion == "" {
		return nil, fmt.Errorf("provider capabilities must include provider version")
	}
	resumeMode := caps.UploadResumeMode
	if resumeMode == "" {
		// Every SDK server supports a durable client-owned idempotency token and
		// safe retransmission. Providers opt into byte-offset resume separately.
		resumeMode = "restart"
	}
	if resumeMode != "restart" && resumeMode != "offset" {
		return nil, fmt.Errorf("provider capabilities include unsupported upload resume mode %q", resumeMode)
	}
	if resumeMode == "offset" {
		if _, ok := s.provider.(ResumableProvider); !ok {
			return nil, fmt.Errorf("provider advertises offset upload resume without implementing ResumableProvider")
		}
	}
	return &providerv1.CapabilitiesResponse{
		ProviderName:     caps.ProviderName,
		ProviderVersion:  caps.ProviderVersion,
		Formats:          caps.Formats,
		OsFamilies:       caps.OSFamilies,
		BuildModes:       caps.BuildModes,
		ProtocolVersion:  ProtocolVersion,
		UploadResumeMode: resumeMode,
	}, nil
}

func (s *Server) ValidateConfig(ctx context.Context, req *providerv1.ValidateConfigRequest) (*providerv1.ValidateConfigResponse, error) {
	if err := s.provider.ValidateConfig(ctx, Config{
		ProviderConfigName: req.GetProviderConfigName(),
		Credentials:        cloneBytesMap(req.GetCredentials()),
		Region:             req.GetRegion(),
		Endpoint:           req.GetEndpoint(),
		Insecure:           req.GetInsecure(),
		Extra:              cloneStringMap(req.GetExtra()),
	}); err != nil {
		return &providerv1.ValidateConfigResponse{Valid: false, Message: err.Error()}, nil
	}
	return &providerv1.ValidateConfigResponse{Valid: true}, nil
}

func (s *Server) UploadArtifact(stream providerv1.PlatformProvider_UploadArtifactServer) error {
	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive first upload chunk: %w", err)
	}
	artifact := ArtifactInfo{
		Format:             first.GetFormat(),
		Checksum:           first.GetChecksum(),
		TotalSizeBytes:     first.GetTotalSizeBytes(),
		OSFamily:           first.GetOsFamily(),
		Metadata:           cloneStringMap(first.GetMetadata()),
		ProviderConfigName: first.GetProviderConfigName(),
		IdempotencyKey:     first.GetIdempotencyKey(),
	}
	if artifact.TotalSizeBytes < 0 {
		return fmt.Errorf("upload total size must not be negative")
	}

	session := UploadSession{}
	resumable, sessionProtocol := s.provider.(ResumableProvider)
	if first.GetIdempotencyKey() != "" {
		session = UploadSession{
			IdempotencyKey:  first.GetIdempotencyKey(),
			ResumeToken:     first.GetSessionToken(),
			CommittedOffset: first.GetResumeOffset(),
			ResumeMode:      "restart",
		}
		if session.CommittedOffset < 0 || session.CommittedOffset > artifact.TotalSizeBytes {
			return fmt.Errorf("resume offset %d is outside artifact size %d", session.CommittedOffset, artifact.TotalSizeBytes)
		}
		if sessionProtocol {
			session, err = resumable.PrepareUpload(stream.Context(), artifact, session)
			if err != nil {
				return fmt.Errorf("prepare resumable upload: %w", err)
			}
		} else {
			session.ResumeToken = session.IdempotencyKey
			session.CommittedOffset = 0
		}
		if err := validateUploadSession(session, artifact.TotalSizeBytes); err != nil {
			return err
		}
		if err := stream.Send(&providerv1.UploadProgress{
			TotalBytes:      artifact.TotalSizeBytes,
			Phase:           "session",
			Message:         "upload session accepted",
			SessionToken:    session.ResumeToken,
			CommittedOffset: session.CommittedOffset,
			ResumeMode:      session.ResumeMode,
		}); err != nil {
			return fmt.Errorf("acknowledge upload session: %w", err)
		}
	}

	reader, writer := io.Pipe()
	copyErr := make(chan error, 1)
	go func() {
		copyErr <- copyUploadChunks(stream, writer, first, artifact.TotalSizeBytes, session.CommittedOffset, first.GetIdempotencyKey() != "")
	}()

	reporter := uploadProgressReporter{stream: stream, session: session}
	var result UploadResult
	var uploadErr error
	if sessionProtocol && first.GetIdempotencyKey() != "" {
		result, uploadErr = resumable.UploadArtifactResumable(stream.Context(), artifact, session, reader, reporter)
	} else {
		result, uploadErr = s.provider.UploadArtifact(stream.Context(), artifact, reader, reporter)
	}
	if uploadErr != nil {
		_ = reader.CloseWithError(uploadErr)
		<-copyErr
		return uploadErr
	}
	if err := <-copyErr; err != nil {
		return err
	}
	if result.ProviderRef == "" {
		return fmt.Errorf("upload result provider ref is required")
	}
	return stream.Send(&providerv1.UploadProgress{
		BytesWritten:    first.GetTotalSizeBytes(),
		TotalBytes:      first.GetTotalSizeBytes(),
		Phase:           "done",
		Message:         "upload completed",
		ProviderRef:     result.ProviderRef,
		SessionToken:    session.ResumeToken,
		CommittedOffset: first.GetTotalSizeBytes(),
		ResumeMode:      session.ResumeMode,
	})
}

func (s *Server) RegisterImage(ctx context.Context, req *providerv1.RegisterRequest) (*providerv1.ImageRef, error) {
	ref, err := s.provider.RegisterImage(ctx, RegisterInput{
		ProviderRef:        req.GetProviderRef(),
		ImageName:          req.GetImageName(),
		Tags:               cloneStringMap(req.GetTags()),
		ProviderConfigName: req.GetProviderConfigName(),
		Format:             req.GetFormat(),
	})
	if err != nil {
		return nil, err
	}
	return &providerv1.ImageRef{
		Id:       ref.ID,
		Name:     ref.Name,
		Location: ref.Location,
		Tags:     cloneStringMap(ref.Tags),
	}, nil
}

func (s *Server) DeleteArtifact(ctx context.Context, req *providerv1.DeleteRequest) (*providerv1.DeleteResponse, error) {
	deleted, message, err := s.provider.DeleteArtifact(ctx, DeleteInput{
		ProviderRef:        req.GetProviderRef(),
		ProviderConfigName: req.GetProviderConfigName(),
	})
	if err != nil {
		return nil, err
	}
	return &providerv1.DeleteResponse{Deleted: deleted, Message: message}, nil
}

func (s *Server) HealthCheck(ctx context.Context, _ *providerv1.Empty) (*providerv1.HealthResponse, error) {
	message, err := s.provider.HealthCheck(ctx)
	if err != nil {
		return &providerv1.HealthResponse{Healthy: false, Message: err.Error()}, nil
	}
	return &providerv1.HealthResponse{Healthy: true, Message: message}, nil
}

func (s *Server) ReconcileRemoteBuild(ctx context.Context, req *providerv1.RemoteBuildRequest) (*providerv1.RemoteBuildResponse, error) {
	remoteProvider, ok := s.provider.(RemoteBuildProvider)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "remote build is not implemented by this provider")
	}
	result, err := remoteProvider.ReconcileRemoteBuild(ctx, remoteBuildInputFromProto(req))
	if err != nil {
		if providererrors.IsTransient(err) {
			retryStatus := status.New(codes.Unavailable, "provider remote build is temporarily unavailable")
			if retryAfter := providererrors.RetryAfter(err); retryAfter > 0 {
				withDetails, detailErr := retryStatus.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)})
				if detailErr == nil {
					retryStatus = withDetails
				}
			}
			return nil, retryStatus.Err()
		}
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	resp := &providerv1.RemoteBuildResponse{
		OperationRef: result.OperationRef,
		Phase:        result.Phase,
		Message:      result.Message,
		Done:         result.Done,
	}
	if result.Artifact != nil {
		resp.Artifact = &providerv1.RemoteArtifact{
			Path:      result.Artifact.Path,
			Format:    result.Artifact.Format,
			Checksum:  result.Artifact.Checksum,
			SizeBytes: result.Artifact.SizeBytes,
			OsFamily:  result.Artifact.OSFamily,
			Metadata:  cloneStringMap(result.Artifact.Metadata),
		}
	}
	if result.Hygiene != nil {
		resp.Hygiene = &providerv1.RemoteHygieneResult{
			Status:    result.Hygiene.Status,
			Message:   result.Hygiene.Message,
			Checks:    append([]string(nil), result.Hygiene.Checks...),
			ResultRef: result.Hygiene.ResultRef,
		}
	}
	for _, image := range result.Images {
		resp.Images = append(resp.Images, &providerv1.RemoteImageRef{
			Provider:           image.Provider,
			ProviderConfigName: image.ProviderConfigName,
			ImageRef:           image.ImageRef,
			ImageName:          image.ImageName,
			Location:           image.Location,
			Format:             image.Format,
			Checksum:           image.Checksum,
			Tags:               cloneStringMap(image.Tags),
		})
	}
	return resp, nil
}

func (s *Server) CleanupRemoteBuild(ctx context.Context, req *providerv1.RemoteBuildRequest) (*providerv1.RemoteBuildCleanupResponse, error) {
	cleanupProvider, ok := s.provider.(RemoteBuildCleanupProvider)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "remote build cleanup is not implemented by this provider")
	}
	result, err := cleanupProvider.CleanupRemoteBuild(ctx, remoteBuildInputFromProto(req))
	if err != nil {
		return nil, err
	}
	return &providerv1.RemoteBuildCleanupResponse{Cleaned: result.Cleaned, Message: result.Message}, nil
}

func remoteBuildInputFromProto(req *providerv1.RemoteBuildRequest) RemoteBuildInput {
	out := RemoteBuildInput{
		BuildID:            req.GetBuildId(),
		OperationRef:       req.GetOperationRef(),
		ImageName:          req.GetImageName(),
		Namespace:          req.GetNamespace(),
		OSFamily:           req.GetOsFamily(),
		OSDistribution:     req.GetOsDistribution(),
		OSVersion:          req.GetOsVersion(),
		OSArch:             req.GetOsArch(),
		SourceType:         req.GetSourceType(),
		SourceURL:          req.GetSourceUrl(),
		SourceProviderRef:  req.GetSourceProviderRef(),
		SourceMarketplace:  sdkMarketplaceRef(req.GetSourceMarketplace()),
		SourceChecksum:     req.GetSourceChecksum(),
		ProviderConfigName: req.GetProviderConfigName(),
		Format:             req.GetFormat(),
		Tags:               cloneStringMap(req.GetTags()),
		TimeoutSeconds:     req.GetTimeoutSeconds(),
	}
	for _, provisioner := range req.GetProvisioners() {
		out.Provisioners = append(out.Provisioners, RemoteProvisioner{
			Type:      provisioner.GetType(),
			Image:     provisioner.GetImage(),
			Inline:    provisioner.GetInline(),
			Playbook:  provisioner.GetPlaybook(),
			Args:      append([]string(nil), provisioner.GetArgs()...),
			ExtraVars: cloneStringMap(provisioner.GetExtraVars()),
			Source:    sdkRemoteProvisionerSource(provisioner.GetSource()),
		})
	}
	if guest := req.GetGuestAccess(); guest != nil {
		out.GuestAccess = &RemoteGuestAccess{
			Protocol:          guest.GetProtocol(),
			User:              guest.GetUser(),
			GuestPort:         guest.GetGuestPort(),
			GeneratedSSHKey:   guest.GetGeneratedSshKey(),
			GeneratedPassword: guest.GetGeneratedPassword(),
			InjectionMethod:   guest.GetInjectionMethod(),
		}
	}
	return out
}

func sdkMarketplaceRef(ref *providerv1.MarketplaceRef) *MarketplaceRef {
	if ref == nil {
		return nil
	}
	return &MarketplaceRef{
		Publisher: ref.GetPublisher(),
		Offer:     ref.GetOffer(),
		SKU:       ref.GetSku(),
		Version:   ref.GetVersion(),
	}
}

func sdkRemoteProvisionerSource(source *providerv1.RemoteProvisionerSource) *RemoteProvisionerSource {
	if source == nil {
		return nil
	}
	out := &RemoteProvisionerSource{}
	if git := source.GetGit(); git != nil {
		out.Git = &RemoteGitProvisionerSource{
			URL:  git.GetUrl(),
			Ref:  git.GetRef(),
			Path: git.GetPath(),
		}
		if auth := git.GetAuth(); auth != nil {
			out.Git.Auth = &RemoteGitProvisionerAuth{
				Token:    auth.GetToken(),
				Username: auth.GetUsername(),
				Password: auth.GetPassword(),
			}
		}
	}
	return out
}

type uploadProgressReporter struct {
	stream  providerv1.PlatformProvider_UploadArtifactServer
	session UploadSession
}

func (r uploadProgressReporter) Report(_ context.Context, progress Progress) error {
	committedOffset := int64(0)
	if r.session.ResumeMode == "offset" {
		committedOffset = progress.BytesWritten
	}
	return r.stream.Send(&providerv1.UploadProgress{
		BytesWritten:    progress.BytesWritten,
		TotalBytes:      progress.TotalBytes,
		Phase:           progress.Phase,
		Message:         progress.Message,
		SessionToken:    r.session.ResumeToken,
		CommittedOffset: committedOffset,
		ResumeMode:      r.session.ResumeMode,
	})
}

func copyUploadChunks(stream providerv1.PlatformProvider_UploadArtifactServer, writer *io.PipeWriter, first *providerv1.UploadChunk, totalSize, startOffset int64, sessionProtocol bool) error {
	expectedOffset := startOffset
	writeChunk := func(chunk *providerv1.UploadChunk) error {
		if chunk.GetOffset() != expectedOffset {
			return fmt.Errorf("upload chunk offset %d does not match expected offset %d", chunk.GetOffset(), expectedOffset)
		}
		if int64(len(chunk.GetData())) > totalSize-expectedOffset {
			return fmt.Errorf("upload chunk at offset %d exceeds artifact size %d", expectedOffset, totalSize)
		}
		if len(chunk.GetData()) > 0 {
			if _, err := writer.Write(chunk.GetData()); err != nil {
				return err
			}
			expectedOffset += int64(len(chunk.GetData()))
		}
		if chunk.GetLast() && expectedOffset != totalSize {
			return fmt.Errorf("final upload chunk ended at offset %d, want %d", expectedOffset, totalSize)
		}
		return nil
	}
	defer writer.Close()
	if !sessionProtocol {
		if err := writeChunk(first); err != nil {
			_ = writer.CloseWithError(err)
			return err
		}
		if first.GetLast() {
			return nil
		}
	}
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			err = fmt.Errorf("upload stream ended at offset %d without final chunk", expectedOffset)
			_ = writer.CloseWithError(err)
			return err
		}
		if err != nil {
			_ = writer.CloseWithError(err)
			return fmt.Errorf("receive upload chunk: %w", err)
		}
		if err := writeChunk(chunk); err != nil {
			_ = writer.CloseWithError(err)
			return err
		}
		if chunk.GetLast() {
			return nil
		}
	}
}

func validateUploadSession(session UploadSession, totalSize int64) error {
	if session.IdempotencyKey == "" || session.ResumeToken == "" {
		return fmt.Errorf("upload session idempotency key and resume token are required")
	}
	if session.ResumeMode != "restart" && session.ResumeMode != "offset" {
		return fmt.Errorf("unsupported upload resume mode %q", session.ResumeMode)
	}
	if session.CommittedOffset < 0 || session.CommittedOffset > totalSize {
		return fmt.Errorf("committed offset %d is outside artifact size %d", session.CommittedOffset, totalSize)
	}
	if session.ResumeMode == "restart" && session.CommittedOffset != 0 {
		return fmt.Errorf("restart upload session must begin at offset zero")
	}
	return nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneBytesMap(in map[string][]byte) map[string][]byte {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}
