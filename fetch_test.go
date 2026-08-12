package viperssm

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// isolateAWSConfig keeps client construction from reading this machine's AWS
// configuration, so the region a test asserts on is the region it asked for.
func isolateAWSConfig(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

// TestClientForReadsTheEndpointAsRegionOrURL settles the ambiguity in Viper's
// positional API: "endpoint" reads as a region name to some people and as a URL
// to others, so decide by whether the string is a URL.
func TestClientForReadsTheEndpointAsRegionOrURL(t *testing.T) {
	tests := []struct {
		name             string
		region           string
		endpoint         string
		wantRegion       string
		wantBaseEndpoint string
	}{
		{
			name:       "a plain endpoint is a region",
			endpoint:   "eu-central-1",
			wantRegion: "eu-central-1",
		},
		{
			name:       "an empty endpoint falls back to WithRegion",
			region:     "us-east-1",
			endpoint:   "",
			wantRegion: "us-east-1",
		},
		{
			name:       "the endpoint wins over WithRegion",
			region:     "us-east-1",
			endpoint:   "ap-southeast-2",
			wantRegion: "ap-southeast-2",
		},
		{
			name:             "an endpoint with a scheme is a URL",
			region:           "eu-central-1",
			endpoint:         "https://vpce-0123.ssm.eu-central-1.vpce.amazonaws.com",
			wantRegion:       "eu-central-1",
			wantBaseEndpoint: "https://vpce-0123.ssm.eu-central-1.vpce.amazonaws.com",
		},
		{
			name:             "a local test double is a URL too",
			endpoint:         "http://localhost:4566",
			wantBaseEndpoint: "http://localhost:4566",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWSConfig(t)

			p, err := New(WithRegion(tt.region))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			reader, err := p.clientFor(context.Background(), tt.endpoint)
			if err != nil {
				t.Fatalf("clientFor(%q): %v", tt.endpoint, err)
			}
			client, ok := reader.(*ssm.Client)
			if !ok {
				t.Fatalf("clientFor returned %T, want *ssm.Client", reader)
			}

			options := client.Options()
			if options.Region != tt.wantRegion {
				t.Errorf("Region = %q, want %q", options.Region, tt.wantRegion)
			}
			if got := aws.ToString(options.BaseEndpoint); got != tt.wantBaseEndpoint {
				t.Errorf("BaseEndpoint = %q, want %q", got, tt.wantBaseEndpoint)
			}
		})
	}
}

// TestClientForUsesWithEndpoint covers the case a per-provider endpoint cannot
// reach: Versions takes a prefix and no endpoint, so without WithEndpoint there
// is no way to point it at a VPC endpoint or an emulator.
func TestClientForUsesWithEndpoint(t *testing.T) {
	isolateAWSConfig(t)

	p, err := New(WithRegion("eu-central-1"), WithEndpoint("http://localhost:4566"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// An empty endpoint is what Versions passes.
	reader, err := p.clientFor(context.Background(), "")
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	client, ok := reader.(*ssm.Client)
	if !ok {
		t.Fatalf("clientFor returned %T, want *ssm.Client", reader)
	}

	options := client.Options()
	if got, want := aws.ToString(options.BaseEndpoint), "http://localhost:4566"; got != want {
		t.Errorf("BaseEndpoint = %q, want %q", got, want)
	}
	if got, want := options.Region, "eu-central-1"; got != want {
		t.Errorf("Region = %q, want %q. An endpoint URL changes where the client connects, not what it signs for", got, want)
	}
}

// TestClientForProviderEndpointOverridesWithEndpoint keeps the per-provider
// value authoritative when both are set.
func TestClientForProviderEndpointOverridesWithEndpoint(t *testing.T) {
	isolateAWSConfig(t)

	p, err := New(WithEndpoint("http://localhost:4566"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	reader, err := p.clientFor(context.Background(), "https://vpce-0123.ssm.eu-central-1.vpce.amazonaws.com")
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	client, ok := reader.(*ssm.Client)
	if !ok {
		t.Fatalf("clientFor returned %T, want *ssm.Client", reader)
	}

	want := "https://vpce-0123.ssm.eu-central-1.vpce.amazonaws.com"
	if got := aws.ToString(client.Options().BaseEndpoint); got != want {
		t.Errorf("BaseEndpoint = %q, want %q", got, want)
	}
}

func TestClientForCachesPerEndpoint(t *testing.T) {
	isolateAWSConfig(t)

	p, err := New(WithRegion("eu-central-1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	first, err := p.clientFor(ctx, "eu-central-1")
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	again, err := p.clientFor(ctx, "eu-central-1")
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	if first != again {
		t.Error("clientFor built a second client for the same endpoint")
	}

	other, err := p.clientFor(ctx, "us-east-1")
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	if first == other {
		t.Error("clientFor reused one client across two regions")
	}
}

// TestClientForPrefersTheInjectedClient keeps WithClient authoritative: it
// already carries its own region and endpoint.
func TestClientForPrefersTheInjectedClient(t *testing.T) {
	fake := &fakeSSM{}
	p := newProvider(t, fake)

	for _, endpoint := range []string{"", "eu-central-1", "http://localhost:4566"} {
		got, err := p.clientFor(context.Background(), endpoint)
		if err != nil {
			t.Fatalf("clientFor(%q): %v", endpoint, err)
		}
		if got != ParameterReader(fake) {
			t.Errorf("clientFor(%q) = %T, want the injected client", endpoint, got)
		}
	}
}
