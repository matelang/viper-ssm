package viperssm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// Default settings. Each one is exported, so a caller can read the value instead
// of guessing it, and so changing one is a visible API change.
const (
	// DefaultPollInterval is how often WatchChannel re-reads a parameter path
	// when WithPollInterval is not set. One poll costs one GetParametersByPath
	// call per page of ten parameters. A path of thirty parameters therefore
	// costs three calls a minute.
	DefaultPollInterval = time.Minute

	// DefaultTimeout bounds one read of a parameter path. Viper's remote
	// interface carries no context, so Get and Watch need a deadline of their
	// own. Versions takes a context and uses the caller's deadline.
	DefaultTimeout = 30 * time.Second
)

// ParameterReader is the only AWS surface this package needs. *ssm.Client
// satisfies it. So does a fake, which is why the tests here need no network and
// no AWS account.
type ParameterReader interface {
	GetParametersByPath(ctx context.Context, params *ssm.GetParametersByPathInput,
		optFns ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error)
}

// Option configures a Provider. Viper's RemoteProvider carries four strings and no
// options struct. So anything richer than a region and a path is set once here,
// not per provider.
type Option func(*Provider) error

// WithRegion sets the AWS region used when a remote provider registers an empty
// endpoint. The default is the region the AWS SDK resolves on its own, from
// AWS_REGION, the shared config file or instance metadata.
//
// A URL is an error here, not a region. Use WithEndpoint.
func WithRegion(region string) Option {
	return func(p *Provider) error {
		if isURL(region) {
			return fmt.Errorf("viperssm: WithRegion: %q is a URL, not a region name; use WithEndpoint for that", region)
		}
		p.region = region
		return nil
	}
}

// WithEndpoint sets the SSM endpoint URL for every remote provider that does not
// carry one of its own, which is what a VPC endpoint or a local emulator needs.
//
// It is also the only way to point Versions at such an endpoint, because
// Versions takes a prefix and no endpoint. Pair it with WithRegion: an endpoint
// URL replaces where the client connects, not which region it signs for.
func WithEndpoint(endpoint string) Option {
	return func(p *Provider) error {
		if !isURL(endpoint) {
			return fmt.Errorf("viperssm: WithEndpoint: %q is not a URL; pass a region name to WithRegion instead", endpoint)
		}
		p.endpoint = endpoint
		return nil
	}
}

// WithClient supplies the SSM client to read through. It overrides WithRegion,
// WithEndpoint and any per-provider endpoint, because the client already carries
// them. Pass a fake in tests, or a client you configured yourself.
func WithClient(c ParameterReader) Option {
	return func(p *Provider) error {
		if c == nil {
			return errors.New("viperssm: WithClient: client is nil")
		}
		p.client = c
		return nil
	}
}

// WithDecryption asks SSM to decrypt SecureString parameters. It is on by
// default. The caller's IAM role then needs kms:Decrypt for the parameter's key.
// Turn it off and values arrive as ciphertext, which a configuration reader
// rarely wants.
//
// Versions ignores this setting and never decrypts. It reads metadata only.
func WithDecryption(on bool) Option {
	return func(p *Provider) error {
		p.decryption = on
		return nil
	}
}

// WithRecursive reads every level below the path. It is on by default. Turn it
// off and only parameters one level below the path are read. Deeper ones then
// become invisible, not an error.
func WithRecursive(on bool) Option {
	return func(p *Provider) error {
		p.recursive = on
		return nil
	}
}

// WithPollInterval sets how often WatchChannel re-reads a parameter path. The
// default is DefaultPollInterval. Every poll costs at least one
// GetParametersByPath call. A very short interval is an AWS bill.
func WithPollInterval(d time.Duration) Option {
	return func(p *Provider) error {
		if d <= 0 {
			return fmt.Errorf("viperssm: WithPollInterval: interval must be positive, got %s", d)
		}
		p.pollInterval = d
		return nil
	}
}

// WithTimeout bounds one read of a parameter path. The default is DefaultTimeout.
// It exists because Viper's remote interface hands us no context. Without a
// deadline, a hung read would hang Get forever.
func WithTimeout(d time.Duration) Option {
	return func(p *Provider) error {
		if d <= 0 {
			return fmt.Errorf("viperssm: WithTimeout: timeout must be positive, got %s", d)
		}
		p.timeout = d
		return nil
	}
}

// WithRequireNonEmpty turns an empty parameter path into an *EmptyPrefixError.
//
// By default an empty path is an empty document and no error. A path nobody has
// populated yet is not a failure. Use this option when the path must exist, and a
// quietly empty configuration would be worse than a crash.
func WithRequireNonEmpty() Option {
	return func(p *Provider) error {
		p.requireNonEmpty = true
		return nil
	}
}

// WithLogger sets the logger. The default is slog.Default, resolved at each call.
// So a logger installed after Install still applies.
//
// This package logs parameter names, types and versions. It never logs a value.
func WithLogger(l *slog.Logger) Option {
	return func(p *Provider) error {
		if l == nil {
			return errors.New("viperssm: WithLogger: logger is nil")
		}
		p.logger = l
		return nil
	}
}
