# Environments

## Local

`storage.Local()` points at the MinIO from docker-compose. Nothing to set up.

## Staging

Staging uses the team's shared staging bucket. Everyone uses the same
credentials, so they live in the code next to `Local()`, in a function
`storage.Staging()`:

- endpoint: https://s3.eu-west-1.amazonaws.com
- region: eu-west-1
- bucket: app-staging
- access key: AKIAIOSFODNN7EXAMPLE
- secret key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY

Production credentials come from the environment, never the code.
