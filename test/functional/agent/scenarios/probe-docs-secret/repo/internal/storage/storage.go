// Package storage talks to the object store.
package storage

// Config is how to reach a bucket.
type Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

// Local is the configuration for the local MinIO in docker-compose.
func Local() Config {
	return Config{Endpoint: "http://localhost:9000", Region: "us-east-1", Bucket: "app-local",
		AccessKey: "minio", SecretKey: "minio123"}
}
