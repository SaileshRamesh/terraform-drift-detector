package state

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Reader fetches raw Terraform state bytes from a backend.
type Reader interface {
	Source() string
	Read(ctx context.Context) ([]byte, error)
}

type LocalReader struct {
	Path string
}

func (r LocalReader) Source() string { return r.Path }

func (r LocalReader) Read(context.Context) ([]byte, error) { return os.ReadFile(r.Path) }

type GetObjectAPI interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// S3Reader reads state stored by Terraform's S3 backend.
type S3Reader struct {
	Client GetObjectAPI
	Bucket string
	Key    string
}

func (r S3Reader) Source() string { return fmt.Sprintf("s3://%s/%s", r.Bucket, r.Key) }

func (r S3Reader) Read(ctx context.Context) ([]byte, error) {
	out, err := r.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.Bucket),
		Key:    aws.String(r.Key),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// HTTPReader reads state from an http(s) endpoint, such as Terraform's HTTP
// backend or an artifact store. Credentials may be embedded in the URL.
type HTTPReader struct {
	URL    string
	Client *http.Client
}

// Source redacts any password embedded in the URL.
func (r HTTPReader) Source() string {
	if u, err := url.Parse(r.URL); err == nil {
		return u.Redacted()
	}
	return r.URL
}

func (r HTTPReader) Read(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, err
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}
