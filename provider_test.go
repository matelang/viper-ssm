package viperssm

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/spf13/viper"
)

func TestGetFollowsPagination(t *testing.T) {
	fake := &fakeSSM{}
	fake.setPages(
		[]types.Parameter{strParam("/app/prod/db/host", "db.example", 1)},
		[]types.Parameter{strParam("/app/prod/db/port", "5432", 1)},
		[]types.Parameter{strParam("/app/prod/log_level", "info", 1)},
	)
	p := newProvider(t, fake)

	got := readDocument(t, p, ssmProvider("/app/prod"))
	want := `{"db":{"host":"db.example","port":"5432"},"log_level":"info"}`
	if got != want {
		t.Errorf("document = %s, want %s", got, want)
	}
	if fake.callCount() != 3 {
		t.Errorf("made %d calls, want 3 (one per page)", fake.callCount())
	}
}

func TestGetSendsTheRequestTheOptionsAskFor(t *testing.T) {
	fake := &fakeSSM{}
	p := newProvider(t, fake, WithRecursive(false), WithDecryption(false))

	if _, err := p.Get(ssmProvider("/app/prod/")); err != nil {
		t.Fatalf("Get: %v", err)
	}

	inputs := fake.inputs()
	if len(inputs) != 1 {
		t.Fatalf("made %d calls, want 1", len(inputs))
	}
	in := inputs[0]
	if got := aws.ToString(in.Path); got != "/app/prod" {
		t.Errorf("Path = %q, want %q (the trailing slash should be normalised away)", got, "/app/prod")
	}
	if aws.ToBool(in.Recursive) {
		t.Error("Recursive = true, want false")
	}
	if aws.ToBool(in.WithDecryption) {
		t.Error("WithDecryption = true, want false")
	}
	if got := aws.ToInt32(in.MaxResults); got != maxResultsPerPage {
		t.Errorf("MaxResults = %d, want %d", got, maxResultsPerPage)
	}
}

func TestGetDefaultsToRecursiveAndDecrypted(t *testing.T) {
	fake := &fakeSSM{}
	p := newProvider(t, fake)

	if _, err := p.Get(ssmProvider("/app")); err != nil {
		t.Fatalf("Get: %v", err)
	}

	in := fake.inputs()[0]
	if !aws.ToBool(in.Recursive) {
		t.Error("Recursive defaults to false, want true")
	}
	if !aws.ToBool(in.WithDecryption) {
		t.Error("WithDecryption defaults to false, want true")
	}
}

// TestGetMissingPrefixIsEmptyNotAnError and the test below it are two halves of one
// rule. Conflate them and a configuration outage looks like an empty
// configuration.
func TestGetMissingPrefixIsEmptyNotAnError(t *testing.T) {
	p := newProvider(t, &fakeSSM{})

	if got, want := readDocument(t, p, ssmProvider("/nothing/here")), `{}`; got != want {
		t.Errorf("document = %s, want %s", got, want)
	}
}

func TestGetPermissionsFailureIsAnError(t *testing.T) {
	denied := errors.New("AccessDeniedException: User is not authorized to perform ssm:GetParametersByPath")
	fake := &fakeSSM{}
	fake.setErr(denied)
	p := newProvider(t, fake)

	reader, err := p.Get(ssmProvider("/app/prod"))
	if err == nil {
		t.Fatalf("Get returned no error; document = %s", mustRead(t, reader))
	}
	if !errors.Is(err, denied) {
		t.Errorf("error %v does not wrap the AWS error", err)
	}
	if !strings.Contains(err.Error(), "/app/prod") {
		t.Errorf("error %q does not name the path it failed on", err)
	}
}

func TestGetRequireNonEmpty(t *testing.T) {
	p := newProvider(t, &fakeSSM{}, WithRequireNonEmpty())

	_, err := p.Get(ssmProvider("/app/prod"))

	var empty *EmptyPrefixError
	if !errors.As(err, &empty) {
		t.Fatalf("Get error = %v, want *EmptyPrefixError", err)
	}
	if empty.Prefix != "/app/prod" {
		t.Errorf("Prefix = %q, want %q", empty.Prefix, "/app/prod")
	}
}

func TestGetEmptyPath(t *testing.T) {
	p := newProvider(t, &fakeSSM{})

	_, err := p.Get(remoteProvider{provider: ProviderName, endpoint: "eu-central-1"})
	if !errors.Is(err, ErrEmptyPath) {
		t.Fatalf("Get error = %v, want ErrEmptyPath", err)
	}
}

// TestGetStopsOnRepeatedPageToken guards the pagination loop against a store
// that keeps handing back the same token.
func TestGetStopsOnRepeatedPageToken(t *testing.T) {
	p := newProvider(t, stuckPagerSSM{})

	_, err := p.Get(ssmProvider("/app"))
	if err == nil {
		t.Fatal("Get returned no error on a repeated page token")
	}
	if !strings.Contains(err.Error(), "pagination token repeated") {
		t.Errorf("error = %q, want it to name the repeated token", err)
	}
}

// stuckPagerSSM always returns the same NextToken, which without a guard would
// page forever.
type stuckPagerSSM struct{}

func (stuckPagerSSM) GetParametersByPath(_ context.Context, _ *ssm.GetParametersByPathInput,
	_ ...func(*ssm.Options),
) (*ssm.GetParametersByPathOutput, error) {
	return &ssm.GetParametersByPathOutput{
		Parameters: []types.Parameter{strParam("/app/host", "example", 1)},
		NextToken:  aws.String("always-the-same"),
	}, nil
}

func TestWatchReturnsTheCurrentDocument(t *testing.T) {
	fake := &fakeSSM{}
	fake.set(strParam("/app/db/host", "example", 1))
	p := newProvider(t, fake)

	reader, err := p.Watch(ssmProvider("/app"))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if got, want := mustRead(t, reader), `{"db":{"host":"example"}}`; got != want {
		t.Errorf("document = %s, want %s", got, want)
	}
}

func TestInstallRegistersWithViper(t *testing.T) {
	isolateViper(t)
	viper.RemoteConfig = nil
	viper.SupportedRemoteProviders = []string{"etcd", "consul"}

	if err := Install(WithClient(&fakeSSM{})); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, ok := viper.RemoteConfig.(*Provider); !ok {
		t.Errorf("viper.RemoteConfig = %T, want *viperssm.Provider", viper.RemoteConfig)
	}
	if !slices.Contains(viper.SupportedRemoteProviders, ProviderName) {
		t.Errorf("SupportedRemoteProviders = %v, want it to contain %q", viper.SupportedRemoteProviders, ProviderName)
	}
}

func TestInstallTwiceAddsOneProviderStringAndDoesNotStack(t *testing.T) {
	isolateViper(t)
	existing := &recordingFactory{}
	viper.RemoteConfig = existing
	viper.SupportedRemoteProviders = []string{"etcd"}

	for i := range 2 {
		if err := Install(WithClient(&fakeSSM{})); err != nil {
			t.Fatalf("Install %d: %v", i+1, err)
		}
	}

	if got := strings.Count(strings.Join(viper.SupportedRemoteProviders, ","), ProviderName); got != 1 {
		t.Errorf("%q appears %d times in %v, want 1", ProviderName, got, viper.SupportedRemoteProviders)
	}

	current, ok := viper.RemoteConfig.(*Provider)
	if !ok {
		t.Fatalf("viper.RemoteConfig = %T, want *viperssm.Provider", viper.RemoteConfig)
	}
	if current.delegate() != remoteFactory(existing) {
		t.Errorf("delegate = %T, want the factory that was registered first; installing twice must not stack", current.delegate())
	}
}

// TestInstallDelegates covers the other half of installing. Viper holds one
// factory, so installing has to keep the one already there.
func TestInstallDelegates(t *testing.T) {
	isolateViper(t)
	existing := &recordingFactory{}
	viper.RemoteConfig = existing

	if err := Install(WithClient(&fakeSSM{})); err != nil {
		t.Fatalf("Install: %v", err)
	}
	p, ok := viper.RemoteConfig.(*Provider)
	if !ok {
		t.Fatalf("viper.RemoteConfig = %T, want *viperssm.Provider", viper.RemoteConfig)
	}

	etcd := remoteProvider{provider: "etcd", endpoint: "http://localhost:2379", path: "/app"}
	if _, err := p.Get(etcd); err != nil {
		t.Errorf("Get(etcd): %v", err)
	}
	if _, err := p.Watch(etcd); err != nil {
		t.Errorf("Watch(etcd): %v", err)
	}
	p.WatchChannel(etcd)

	got, watched, streamed := existing.calls()
	for name, calls := range map[string][]string{"Get": got, "Watch": watched, "WatchChannel": streamed} {
		if !slices.Equal(calls, []string{"etcd"}) {
			t.Errorf("delegated %s calls = %v, want [etcd]", name, calls)
		}
	}
}

func TestUnhandledProviderWithoutADelegate(t *testing.T) {
	p := newProvider(t, &fakeSSM{})
	etcd := remoteProvider{provider: "etcd", endpoint: "http://localhost:2379", path: "/app"}

	var unhandled *UnhandledProviderError

	if _, err := p.Get(etcd); !errors.As(err, &unhandled) {
		t.Errorf("Get error = %v, want *UnhandledProviderError", err)
	}
	if _, err := p.Watch(etcd); !errors.As(err, &unhandled) {
		t.Errorf("Watch error = %v, want *UnhandledProviderError", err)
	}

	// WatchChannel cannot return an error, so it delivers one on the channel.
	responses, stop := p.WatchChannel(etcd)
	select {
	case response := <-responses:
		if !errors.As(response.Error, &unhandled) {
			t.Errorf("response error = %v, want *UnhandledProviderError", response.Error)
		}
	default:
		t.Error("WatchChannel delivered no error for an unhandled provider")
	}
	close(stop)

	if unhandled.Provider != "etcd" {
		t.Errorf("Provider = %q, want %q", unhandled.Provider, "etcd")
	}
	if !strings.Contains(unhandled.Error(), "viper/remote") {
		t.Errorf("error %q does not say how to keep Viper's own providers working", unhandled)
	}
}

// TestReadRemoteConfigThroughViper is the end-to-end path a user takes, including
// the SetConfigType("json") that is easy to forget.
func TestReadRemoteConfigThroughViper(t *testing.T) {
	isolateViper(t)
	viper.RemoteConfig = nil
	viper.SupportedRemoteProviders = []string{}

	fake := &fakeSSM{}
	fake.set(
		strParam("/myapp/prod/database/url", "postgres://db.example/app", 3),
		strParam("/myapp/prod/http/port", "8080", 1),
		listParam("/myapp/prod/http/hosts", "a.example,b.example", 1),
	)
	if err := Install(WithClient(fake)); err != nil {
		t.Fatalf("Install: %v", err)
	}

	v := viper.New()
	v.SetConfigType("json")
	if err := v.AddRemoteProvider(ProviderName, "eu-central-1", "/myapp/prod"); err != nil {
		t.Fatalf("AddRemoteProvider: %v", err)
	}
	if err := v.ReadRemoteConfig(); err != nil {
		t.Fatalf("ReadRemoteConfig: %v", err)
	}

	if got, want := v.GetString("database.url"), "postgres://db.example/app"; got != want {
		t.Errorf("database.url = %q, want %q", got, want)
	}
	if got, want := v.GetInt("http.port"), 8080; got != want {
		t.Errorf("http.port = %d, want %d", got, want)
	}
	if got, want := v.GetStringSlice("http.hosts"), []string{"a.example", "b.example"}; !slices.Equal(got, want) {
		t.Errorf("http.hosts = %v, want %v", got, want)
	}
}

// TestWatchRemoteConfigThroughViper covers Viper's synchronous re-read.
func TestWatchRemoteConfigThroughViper(t *testing.T) {
	isolateViper(t)
	viper.RemoteConfig = nil
	viper.SupportedRemoteProviders = []string{}

	fake := &fakeSSM{}
	fake.set(strParam("/myapp/prod/log_level", "info", 1))
	if err := Install(WithClient(fake)); err != nil {
		t.Fatalf("Install: %v", err)
	}

	v := viper.New()
	v.SetConfigType("json")
	if err := v.AddRemoteProvider(ProviderName, "eu-central-1", "/myapp/prod"); err != nil {
		t.Fatalf("AddRemoteProvider: %v", err)
	}
	if err := v.ReadRemoteConfig(); err != nil {
		t.Fatalf("ReadRemoteConfig: %v", err)
	}

	fake.set(strParam("/myapp/prod/log_level", "debug", 2))
	if err := v.WatchRemoteConfig(); err != nil {
		t.Fatalf("WatchRemoteConfig: %v", err)
	}

	if got, want := v.GetString("log_level"), "debug"; got != want {
		t.Errorf("log_level = %q, want %q", got, want)
	}
}

// TestReadRemoteConfigWithoutConfigTypeIsEmpty pins the failure mode the README
// leads with. Viper parses a remote document with the configured config type, so
// forgetting SetConfigType yields an empty configuration and no error.
func TestReadRemoteConfigWithoutConfigTypeIsEmpty(t *testing.T) {
	isolateViper(t)
	viper.RemoteConfig = nil
	viper.SupportedRemoteProviders = []string{}

	fake := &fakeSSM{}
	fake.set(strParam("/myapp/prod/database/url", "postgres://db.example/app", 1))
	if err := Install(WithClient(fake)); err != nil {
		t.Fatalf("Install: %v", err)
	}

	v := viper.New() // no SetConfigType
	if err := v.AddRemoteProvider(ProviderName, "eu-central-1", "/myapp/prod"); err != nil {
		t.Fatalf("AddRemoteProvider: %v", err)
	}
	err := v.ReadRemoteConfig()

	if err == nil && v.GetString("database.url") != "" {
		t.Fatal("this test is stale: Viper now reads a remote document without SetConfigType, so the README warning can go")
	}
}

func TestOptionsReject(t *testing.T) {
	tests := []struct {
		name string
		opt  Option
	}{
		{name: "nil client", opt: WithClient(nil)},
		{name: "nil logger", opt: WithLogger(nil)},
		{name: "zero poll interval", opt: WithPollInterval(0)},
		{name: "negative poll interval", opt: WithPollInterval(-time.Second)},
		{name: "zero timeout", opt: WithTimeout(0)},
		// A URL in WithRegion reaches the SDK as a region and fails there with
		// "invalid input region", far from the call that caused it.
		{name: "a URL passed as a region", opt: WithRegion("http://localhost:4566")},
		{name: "a region passed as an endpoint", opt: WithEndpoint("eu-central-1")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.opt); err == nil {
				t.Error("New accepted the option, want an error")
			}
			if err := Install(tt.opt); err == nil {
				t.Error("Install accepted the option, want an error")
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !p.decryption {
		t.Error("decryption defaults to off, want on")
	}
	if !p.recursive {
		t.Error("recursive defaults to off, want on")
	}
	if p.pollInterval != DefaultPollInterval {
		t.Errorf("pollInterval = %s, want %s", p.pollInterval, DefaultPollInterval)
	}
	if p.timeout != DefaultTimeout {
		t.Errorf("timeout = %s, want %s", p.timeout, DefaultTimeout)
	}
	if p.requireNonEmpty {
		t.Error("requireNonEmpty defaults to on, want off")
	}
}

func mustRead(t *testing.T, r io.Reader) string {
	t.Helper()
	if r == nil {
		return "<nil reader>"
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}
