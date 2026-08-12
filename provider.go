package viperssm

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/spf13/viper"
)

// ProviderName is the provider string to pass to viper.AddRemoteProvider.
const ProviderName = "ssm"

// remoteFactory mirrors viper's unexported remoteConfigFactory. Viper keeps
// exactly one factory in viper.RemoteConfig, so installing ours has to keep
// whatever is already there and delegate to it.
//
// The type is unexported over there. All three of its methods are exported, so an
// identical interface declared here is assignable in both directions.
type remoteFactory interface {
	Get(rp viper.RemoteProvider) (io.Reader, error)
	Watch(rp viper.RemoteProvider) (io.Reader, error)
	WatchChannel(rp viper.RemoteProvider) (<-chan *viper.RemoteResponse, chan bool)
}

// Compile-time proof that the two interfaces still match. A Viper release that
// changes remoteConfigFactory breaks this build, not a user's runtime.
var (
	_ remoteFactory = (*Provider)(nil)
	_ remoteFactory = viper.RemoteConfig
)

// Provider reads configuration from AWS SSM Parameter Store on Viper's behalf. It
// satisfies the interface Viper expects in viper.RemoteConfig.
//
// Build one with New. Register it with Install. A Provider is safe for concurrent
// use once built.
type Provider struct {
	client          ParameterReader
	region          string
	endpoint        string
	decryption      bool
	recursive       bool
	requireNonEmpty bool
	pollInterval    time.Duration
	timeout         time.Duration
	logger          *slog.Logger

	mu       sync.RWMutex
	next     remoteFactory
	clients  map[string]ParameterReader
	versions map[string]map[string]int64
}

// New builds a Provider. It reports the first option that does not apply.
//
// New does not touch Viper's globals. Call Install, or assign the Provider to
// viper.RemoteConfig yourself.
func New(opts ...Option) (*Provider, error) {
	p := &Provider{
		decryption:   true,
		recursive:    true,
		pollInterval: DefaultPollInterval,
		timeout:      DefaultTimeout,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// log returns the logger to write to. slog.Default is resolved per call so that
// a logger installed after Install still applies.
func (p *Provider) log() *slog.Logger {
	if p.logger != nil {
		return p.logger
	}
	return slog.Default()
}

// delegate returns the factory that was registered with Viper before this one.
func (p *Provider) delegate() remoteFactory {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.next
}

// Get reads every parameter under rp.Path() and returns them as a JSON document.
// Viper parses that document with the configured config type, so the caller must
// have called SetConfigType("json").
//
// rp.Endpoint() is the AWS region, or a URL to use as the SSM endpoint.
//
// A path that holds no parameters is an empty document and no error. A permissions
// failure is an error. Conflate the two and a configuration outage looks like an
// empty configuration.
//
// Any provider string other than ProviderName goes to the factory Install kept.
func (p *Provider) Get(rp viper.RemoteProvider) (io.Reader, error) {
	if rp.Provider() != ProviderName {
		next := p.delegate()
		if next == nil {
			return nil, &UnhandledProviderError{Provider: rp.Provider()}
		}
		return next.Get(rp)
	}
	_, doc, err := p.load(context.Background(), rp)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(doc), nil
}

// Watch re-reads the parameter path and returns the current document. Viper calls
// it from WatchRemoteConfig to re-read on demand, so it does the same work as Get.
// For a continuous watch, use WatchChannel.
func (p *Provider) Watch(rp viper.RemoteProvider) (io.Reader, error) {
	if rp.Provider() != ProviderName {
		next := p.delegate()
		if next == nil {
			return nil, &UnhandledProviderError{Provider: rp.Provider()}
		}
		return next.Watch(rp)
	}
	return p.Get(rp)
}

// load reads the path and builds the document. It applies the provider's own
// timeout to ctx, because Viper's remote interface carries no context.
func (p *Provider) load(ctx context.Context, rp viper.RemoteProvider) (map[string]int64, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	prefix, params, err := p.fetch(ctx, rp.Endpoint(), rp.Path(), p.decryption)
	if err != nil {
		return nil, nil, err
	}
	if len(params) == 0 && p.requireNonEmpty {
		return nil, nil, &EmptyPrefixError{Prefix: prefix}
	}

	doc, err := p.document(prefix, params)
	if err != nil {
		return nil, nil, err
	}

	versions := versionsOf(params)
	p.rememberVersions(rp, prefix, versions)
	p.log().Debug("read SSM parameters",
		"prefix", prefix, "count", len(params), "bytes", len(doc))
	return versions, doc, nil
}

// versionKey identifies one endpoint and path pair. One Provider can serve several
// remote providers, and their versions must not mix.
func versionKey(endpoint, prefix string) string {
	return endpoint + "\x00" + prefix
}

func (p *Provider) rememberVersions(rp viper.RemoteProvider, prefix string, versions map[string]int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.versions == nil {
		p.versions = make(map[string]map[string]int64)
	}
	p.versions[versionKey(rp.Endpoint(), prefix)] = versions
}

// knownVersions returns the versions the most recent load of this path recorded.
// It returns nil if nothing has read the path yet.
//
// WatchChannel seeds its baseline from it. That saves one API call at startup.
// More usefully, it means a change made between Get and the first poll still gets
// reported.
func (p *Provider) knownVersions(rp viper.RemoteProvider) map[string]int64 {
	prefix, err := normalizePath(rp.Path())
	if err != nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.versions[versionKey(rp.Endpoint(), prefix)]
}

// Package-level state, so that the package-level Install and Versions can work
// the way the rest of Viper's API does.
var (
	installMu sync.Mutex
	installed *Provider
)

// Install builds a Provider from opts and registers it with Viper. After it
// returns, viper.AddRemoteProvider("ssm", region, path) works.
//
// Installing is additive. Viper holds exactly one remote factory, so Install keeps
// the one already registered and hands it every provider string this one does not
// own. A program that blank-imports github.com/spf13/viper/remote therefore keeps
// etcd, Consul, Firestore and NATS working. Import that package before Install,
// not after. Import it after and it overwrites this one.
//
// Call Install once, during startup, before other goroutines use Viper.
// viper.RemoteConfig and viper.SupportedRemoteProviders are unsynchronised globals
// in Viper itself.
func Install(opts ...Option) error {
	p, err := New(opts...)
	if err != nil {
		return err
	}
	return p.Install()
}

// Install registers p with Viper, and keeps any factory already registered. See
// the package-level Install.
func (p *Provider) Install() error {
	installMu.Lock()
	defer installMu.Unlock()

	// The assignment converts viper's unexported interface to ours. A nil
	// viper.RemoteConfig gives a nil delegate. Get then reports an
	// UnhandledProviderError instead of dereferencing it.
	var previous remoteFactory = viper.RemoteConfig
	if earlier, ok := previous.(*Provider); ok {
		// Installing twice must not stack our own factories. Take over the
		// chain the earlier one held, rather than delegating to it.
		previous = earlier.delegate()
	}

	p.mu.Lock()
	p.next = previous
	p.mu.Unlock()

	viper.RemoteConfig = p
	if !slices.Contains(viper.SupportedRemoteProviders, ProviderName) {
		viper.SupportedRemoteProviders = append(viper.SupportedRemoteProviders, ProviderName)
	}
	installed = p
	return nil
}

// Versions returns the SSM version of every parameter under prefix, keyed by full
// parameter name.
//
// Record the result at boot, call it again later, and compare. An SSM version
// rises by one per edit, so a difference is an exact staleness verdict. The call
// costs one request per page of ten parameters. It reads no plaintext, because
// decryption is off. So it needs no kms:Decrypt permission, and it holds and
// compares no secret value.
//
// It uses the Provider that Install registered. With none, it returns
// ErrNotInstalled. Build a Provider with New to avoid the package-level state.
func Versions(ctx context.Context, prefix string) (map[string]int64, error) {
	installMu.Lock()
	p := installed
	installMu.Unlock()
	if p == nil {
		return nil, ErrNotInstalled
	}
	return p.Versions(ctx, prefix)
}

// Versions returns the SSM version of every parameter under prefix, keyed by
// full parameter name. See the package-level Versions.
//
// Unlike Get, it uses the caller's context and imposes no timeout of its own.
func (p *Provider) Versions(ctx context.Context, prefix string) (map[string]int64, error) {
	_, params, err := p.fetch(ctx, "", prefix, false)
	if err != nil {
		return nil, err
	}
	return versionsOf(params), nil
}
