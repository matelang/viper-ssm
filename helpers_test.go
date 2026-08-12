package viperssm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/spf13/viper"
)

// fakeSSM is a ParameterReader that serves fixed pages, so the tests need no
// network and no AWS account. Pages are addressed by a numeric NextToken.
type fakeSSM struct {
	mu    sync.Mutex
	pages [][]types.Parameter
	err   error
	calls []ssm.GetParametersByPathInput
}

func (f *fakeSSM) GetParametersByPath(_ context.Context, in *ssm.GetParametersByPathInput,
	_ ...func(*ssm.Options),
) (*ssm.GetParametersByPathOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, *in)
	if f.err != nil {
		return nil, f.err
	}

	page := 0
	if in.NextToken != nil {
		n, err := strconv.Atoi(*in.NextToken)
		if err != nil {
			return nil, fmt.Errorf("fakeSSM: unrecognised page token %q", *in.NextToken)
		}
		page = n
	}
	if page >= len(f.pages) {
		return &ssm.GetParametersByPathOutput{}, nil
	}

	out := &ssm.GetParametersByPathOutput{Parameters: f.pages[page]}
	if page+1 < len(f.pages) {
		out.NextToken = aws.String(strconv.Itoa(page + 1))
	}
	return out, nil
}

// setPages replaces what the fake serves. Later calls see the new pages, which
// is how the watch tests make a parameter change.
func (f *fakeSSM) setPages(pages ...[]types.Parameter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages = pages
	f.err = nil
}

// set replaces what the fake serves with a single page.
func (f *fakeSSM) set(params ...types.Parameter) {
	f.setPages(params)
}

func (f *fakeSSM) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeSSM) inputs() []ssm.GetParametersByPathInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeSSM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func strParam(name, value string, version int64) types.Parameter {
	return types.Parameter{
		Name:    aws.String(name),
		Value:   aws.String(value),
		Type:    types.ParameterTypeString,
		Version: version,
	}
}

func secureParam(name, value string, version int64) types.Parameter {
	p := strParam(name, value, version)
	p.Type = types.ParameterTypeSecureString
	return p
}

// namelessParam is the malformed response SSM should never send.
func namelessParam(value string) types.Parameter {
	return types.Parameter{Value: aws.String(value), Type: types.ParameterTypeSecureString, Version: 1}
}

func listParam(name, value string, version int64) types.Parameter {
	p := strParam(name, value, version)
	p.Type = types.ParameterTypeStringList
	return p
}

// remoteProvider is a viper.RemoteProvider built by hand. Tests use it to call
// Get directly, without viper.AddRemoteProvider's rule that the endpoint must
// not be empty.
type remoteProvider struct {
	provider string
	endpoint string
	path     string
	keyring  string
}

func (r remoteProvider) Provider() string      { return r.provider }
func (r remoteProvider) Endpoint() string      { return r.endpoint }
func (r remoteProvider) Path() string          { return r.path }
func (r remoteProvider) SecretKeyring() string { return r.keyring }

func ssmProvider(path string) remoteProvider {
	return remoteProvider{provider: ProviderName, endpoint: "eu-central-1", path: path}
}

// recordingFactory stands in for a factory already registered with Viper, so
// the delegation requirement can be tested without pulling in viper/remote.
type recordingFactory struct {
	mu       sync.Mutex
	got      []string
	watched  []string
	streamed []string
}

func (f *recordingFactory) Get(rp viper.RemoteProvider) (io.Reader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, rp.Provider())
	return strings.NewReader(`{"delegated":true}`), nil
}

func (f *recordingFactory) Watch(rp viper.RemoteProvider) (io.Reader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watched = append(f.watched, rp.Provider())
	return strings.NewReader(`{"delegated":true}`), nil
}

func (f *recordingFactory) WatchChannel(rp viper.RemoteProvider) (<-chan *viper.RemoteResponse, chan bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamed = append(f.streamed, rp.Provider())
	return make(chan *viper.RemoteResponse), make(chan bool)
}

func (f *recordingFactory) calls() (got, watched, streamed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got), slices.Clone(f.watched), slices.Clone(f.streamed)
}

// isolateViper snapshots the Viper globals this package writes and restores
// them when the test ends. Viper does not synchronise them, so these tests must
// not run in parallel.
func isolateViper(t *testing.T) {
	t.Helper()

	factory := viper.RemoteConfig
	providers := slices.Clone(viper.SupportedRemoteProviders)

	installMu.Lock()
	previous := installed
	installMu.Unlock()

	t.Cleanup(func() {
		viper.RemoteConfig = factory
		viper.SupportedRemoteProviders = providers
		installMu.Lock()
		installed = previous
		installMu.Unlock()
	})
}

// newProvider builds a Provider on a fake client, failing the test if an option
// does not apply.
func newProvider(t *testing.T, client ParameterReader, opts ...Option) *Provider {
	t.Helper()
	p, err := New(append([]Option{WithClient(client)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// readDocument calls Get and returns the JSON document as a string.
func readDocument(t *testing.T, p *Provider, rp viper.RemoteProvider) string {
	t.Helper()
	reader, err := p.Get(rp)
	if err != nil {
		t.Fatalf("Get(%q): %v", rp.Path(), err)
	}
	doc, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read document: %v", err)
	}
	return string(doc)
}

// startWatch seeds the version baseline with a Get, the way Viper's
// ReadRemoteConfig does, and then starts the watch. Without the seed, a test
// that changes a parameter straight away races the watch's own baseline read
// and the change can land before there is anything to compare it against.
func startWatch(t *testing.T, p *Provider, rp viper.RemoteProvider) (<-chan *viper.RemoteResponse, chan bool) {
	t.Helper()
	if _, err := p.Get(rp); err != nil {
		t.Fatalf("Get to seed the watch baseline: %v", err)
	}
	return p.WatchChannel(rp)
}

// captureLogs returns a logger that writes everything, including debug, into
// buf. The no-values-in-logs test reads buf to prove no value reached it.
func captureLogs() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(handler), buf
}

// goroutinesMatching counts goroutines whose stack mentions substr. It is how
// the teardown test proves the watch goroutines are gone, without adding a
// dependency on a leak detector.
func goroutinesMatching(substr string) int {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), substr)
		}
		buf = make([]byte, 2*len(buf))
	}
}

// waitFor polls until want reports true, or fails the test.
func waitFor(t *testing.T, timeout time.Duration, what string, want func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if want() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}
