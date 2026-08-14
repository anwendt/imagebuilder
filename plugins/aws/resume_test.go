package aws

import (
	"context"
	"encoding/json"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type resumeS3Fake struct {
	listOutput  *s3.ListPartsOutput
	listErr     error
	createCalls int
}

func (f *resumeS3Fake) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return &s3.PutObjectOutput{}, nil
}
func (f *resumeS3Fake) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, nil
}
func (f *resumeS3Fake) CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	f.createCalls++
	return &s3.CreateMultipartUploadOutput{UploadId: awssdk.String("new-upload")}, nil
}
func (f *resumeS3Fake) UploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	return &s3.UploadPartOutput{ETag: awssdk.String("etag")}, nil
}
func (f *resumeS3Fake) CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	return &s3.CompleteMultipartUploadOutput{}, nil
}
func (f *resumeS3Fake) AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	return &s3.AbortMultipartUploadOutput{}, nil
}
func (f *resumeS3Fake) ListParts(context.Context, *s3.ListPartsInput, ...func(*s3.Options)) (*s3.ListPartsOutput, error) {
	return f.listOutput, f.listErr
}

func TestPrepareMultipartSessionReconstructsAuthoritativeParts(t *testing.T) {
	fake := &resumeS3Fake{listOutput: &s3.ListPartsOutput{Parts: []s3types.Part{
		{PartNumber: awssdk.Int32(1), Size: awssdk.Int64(awsResumePartSize), ETag: awssdk.String("etag-1")},
		{PartNumber: awssdk.Int32(2), Size: awssdk.Int64(awsResumePartSize), ETag: awssdk.String("etag-2")},
	}}}
	client := &awsSDKLocalImageClient{s3: fake}
	persisted, err := json.Marshal(awsMultipartSession{UploadID: "upload-1", Bucket: "bucket", Key: "key", Size: 3 * awsResumePartSize, Offset: awsResumePartSize, Parts: []awsCompletedPart{{Number: 1, ETag: "stale"}}})
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	state, err := client.prepareMultipartSession(context.Background(), "bucket", "key", 3*awsResumePartSize, string(persisted))
	if err != nil {
		t.Fatalf("prepareMultipartSession returned error: %v", err)
	}
	if state.Offset != 2*awsResumePartSize || len(state.Parts) != 2 || state.Parts[1].ETag != "etag-2" {
		t.Fatalf("reconstructed state = %#v", state)
	}
}

func TestPrepareMultipartSessionRestartsMissingUpload(t *testing.T) {
	fake := &resumeS3Fake{listErr: &smithy.GenericAPIError{Code: "NoSuchUpload", Message: "gone", Fault: smithy.FaultClient}}
	client := &awsSDKLocalImageClient{s3: fake}
	persisted, err := json.Marshal(awsMultipartSession{UploadID: "expired", Bucket: "bucket", Key: "key", Size: awsResumePartSize})
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	state, err := client.prepareMultipartSession(context.Background(), "bucket", "key", awsResumePartSize, string(persisted))
	if err != nil {
		t.Fatalf("prepareMultipartSession returned error: %v", err)
	}
	if state.UploadID != "new-upload" || state.Offset != 0 || fake.createCalls != 1 {
		t.Fatalf("new state = %#v, createCalls=%d", state, fake.createCalls)
	}
}
