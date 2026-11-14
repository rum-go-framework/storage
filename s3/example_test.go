package s3_test

import (
	"context"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rum-go-framework/storage"
	"github.com/rum-go-framework/storage/s3"
)

// The app builds the client, so endpoint and addressing style are its call:
// for AWS only the config is needed, for MinIO point BaseEndpoint at it and
// turn on path-style addressing.
func Example() {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatal(err)
	}
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String("http://localhost:9000")
		o.UsePathStyle = true
	})

	st := s3.New(client, "my-bucket", s3.WithPrefix("uploads"))
	var _ storage.Storage = st

	url, err := st.PresignGet(ctx, "avatars/42.png", 15*time.Minute)
	if err != nil {
		log.Fatal(err)
	}
	log.Println(url)
}
