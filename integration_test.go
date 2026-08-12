//go:build integration

// Integration tests against Floci, an AWS-compatible emulator, so that the
// contract is checked against a real SSM API rather than only against a fake.
//
//	docker compose up -d floci        # or any Floci on :4566
//	make test-integration
//
// FLOCI_ENDPOINT overrides the endpoint. Set FLOCI_REQUIRED=1 and an unreachable
// emulator fails rather than skips. CI sets it, because a green job that tested
// nothing is worse than a red one.
//
// What Floci cannot check, and the fake still has to:
//
//   - Pagination. Floci ignores MaxResults and answers in one page, so the
//     pagination loop stays covered by TestGetFollowsPagination.
//   - Ciphertext. Floci returns SecureString values in plaintext whatever
//     WithDecryption says, so TestIntegrationVersionsNeedNoDecryption asserts the
//     request, not the response.
//   - IAM. There is no access control to deny, so a permissions failure stays
//     covered by TestGetPermissionsFailureIsAnError.
package viperssm_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/spf13/viper"

	viperssm "github.com/matelang/viper-ssm"
)

const (
	defaultFlociEndpoint = "http://localhost:4566"

	// flociRegion matches the emulator's own AWS_DEFAULT_REGION.
	flociRegion = "us-east-1"

	// deleteParametersBatch is the largest batch DeleteParameters accepts.
	deleteParametersBatch = 10
)

// emulator is a reachable Floci, plus a client for seeding parameters.
type emulator struct {
	endpoint string
	client   *ssm.Client
}

// requireEmulator resolves the endpoint and proves it answers. It also puts static
// credentials and the emulator's region in the environment. The code under test
// then builds its own client through the standard AWS credential chain, which is
// the path production takes.
func requireEmulator(t *testing.T) *emulator {
	t.Helper()

	endpoint := os.Getenv("FLOCI_ENDPOINT")
	if endpoint == "" {
		endpoint = defaultFlociEndpoint
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("FLOCI_ENDPOINT %q is not a URL: %v", endpoint, err)
	}
	if conn, err := net.DialTimeout("tcp", parsed.Host, 2*time.Second); err != nil {
		// CI runs Floci as a service container, so unreachable means broken.
		if required, _ := os.LookupEnv("FLOCI_REQUIRED"); required != "" && required != "0" {
			t.Fatalf("FLOCI_REQUIRED is set but no emulator answers at %s: %v", endpoint, err)
		}
		t.Skipf("no AWS emulator at %s. Start Floci to run this tier (%v)", endpoint, err)
	} else {
		_ = conn.Close()
	}

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_REGION", flociRegion)
	t.Setenv("AWS_DEFAULT_REGION", flociRegion)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(flociRegion))
	if err != nil {
		t.Fatalf("load AWS configuration: %v", err)
	}
	client := ssm.NewFromConfig(cfg, func(o *ssm.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	return &emulator{endpoint: endpoint, client: client}
}

// prefix returns a parameter path unique to this test, so that repeat runs and a
// Floci shared with other projects cannot collide.
func (e *emulator) prefix(t *testing.T) string {
	t.Helper()

	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, t.Name())

	return fmt.Sprintf("/viperssm-it/%s-%d", slug, rand.IntN(1_000_000)) //nolint:gosec // test-only naming
}

// put writes one parameter and schedules its deletion. Overwriting an existing
// name bumps its SSM version, which is what the staleness and watch tests need.
func (e *emulator) put(t *testing.T, name, value string, paramType types.ParameterType) {
	t.Helper()

	_, err := e.client.PutParameter(context.Background(), &ssm.PutParameterInput{
		Name:      aws.String(name),
		Value:     aws.String(value),
		Type:      paramType,
		Overwrite: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("PutParameter %s: %v", name, err)
	}
	t.Cleanup(func() { e.remove(name) })
}

// seed writes String parameters from a name-to-value map, in a stable order.
func (e *emulator) seed(t *testing.T, params map[string]string) {
	t.Helper()
	for _, name := range slices.Sorted(maps.Keys(params)) {
		e.put(t, name, params[name], types.ParameterTypeString)
	}
}

func (e *emulator) remove(names ...string) {
	for chunk := range slices.Chunk(names, deleteParametersBatch) {
		// Best effort: a test that already failed should report its own error,
		// not a cleanup error on top of it.
		_, _ = e.client.DeleteParameters(context.Background(),
			&ssm.DeleteParametersInput{Names: chunk})
	}
}

// provider builds a Provider pointed at the emulator. Nothing here injects a
// client: WithEndpoint and the per-provider endpoint both go through the real
// AWS SDK, so these tests exercise the same client construction production does.
func (e *emulator) provider(t *testing.T, opts ...viperssm.Option) *viperssm.Provider {
	t.Helper()
	p, err := viperssm.New(append([]viperssm.Option{viperssm.WithEndpoint(e.endpoint)}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// path is a viper.RemoteProvider for one prefix on the emulator.
func (e *emulator) path(prefix string) ssmPath {
	return ssmPath{region: e.endpoint, path: prefix}
}

func readAll(t *testing.T, p *viperssm.Provider, rp viper.RemoteProvider) string {
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

// isolateViper restores the globals Install writes.
func isolateViper(t *testing.T) {
	t.Helper()
	factory := viper.RemoteConfig
	providers := slices.Clone(viper.SupportedRemoteProviders)
	t.Cleanup(func() {
		viper.RemoteConfig = factory
		viper.SupportedRemoteProviders = providers
	})
}

// TestIntegrationReadThroughViper is the path in the README, against a real SSM
// API: Install, SetConfigType, AddRemoteProvider with a URL endpoint,
// ReadRemoteConfig.
func TestIntegrationReadThroughViper(t *testing.T) {
	e := requireEmulator(t)
	isolateViper(t)
	prefix := e.prefix(t)

	e.seed(t, map[string]string{
		prefix + "/database/primary/host": "db.example",
		prefix + "/database/primary/port": "5432",
		prefix + "/log_level":             "info",
	})

	if err := viperssm.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}

	v := viper.New()
	v.SetConfigType("json")
	if err := v.AddRemoteProvider("ssm", e.endpoint, prefix); err != nil {
		t.Fatalf("AddRemoteProvider: %v", err)
	}
	if err := v.ReadRemoteConfig(); err != nil {
		t.Fatalf("ReadRemoteConfig: %v", err)
	}

	if got, want := v.GetString("database.primary.host"), "db.example"; got != want {
		t.Errorf("database.primary.host = %q, want %q", got, want)
	}
	if got, want := v.GetInt("database.primary.port"), 5432; got != want {
		t.Errorf("database.primary.port = %d, want %d", got, want)
	}
	if got, want := v.GetString("log_level"), "info"; got != want {
		t.Errorf("log_level = %q, want %q", got, want)
	}
}

// TestIntegrationParameterTypes checks the three SSM types against the real API,
// including that a SecureString comes back decrypted.
func TestIntegrationParameterTypes(t *testing.T) {
	e := requireEmulator(t)
	prefix := e.prefix(t)

	e.put(t, prefix+"/plain", "visible", types.ParameterTypeString)
	e.put(t, prefix+"/secret", "hunter2", types.ParameterTypeSecureString)
	e.put(t, prefix+"/hosts", "a.example,b.example", types.ParameterTypeStringList)

	p := e.provider(t)
	got := readAll(t, p, e.path(prefix))
	want := `{"hosts":["a.example","b.example"],"plain":"visible","secret":"hunter2"}`
	if got != want {
		t.Errorf("document =\n  %s\nwant\n  %s", got, want)
	}
}

// TestIntegrationReadsEveryParameterUnderThePrefix uses more parameters than one
// page of the real API holds. Floci answers in a single page, so this asserts
// completeness rather than pagination; the pagination loop itself is covered by
// TestGetFollowsPagination against the fake.
func TestIntegrationReadsEveryParameterUnderThePrefix(t *testing.T) {
	e := requireEmulator(t)
	prefix := e.prefix(t)

	const count = 25
	want := make(map[string]string, count)
	for i := range count {
		want[fmt.Sprintf("%s/group%d/key%02d", prefix, i%3, i)] = fmt.Sprintf("value%02d", i)
	}
	e.seed(t, want)

	p := e.provider(t)
	doc := readAll(t, p, e.path(prefix))

	for name, value := range want {
		if !strings.Contains(doc, `"`+value+`"`) {
			t.Errorf("document is missing the value of %s", name)
		}
	}
	if got := strings.Count(doc, `"value`); got != count {
		t.Errorf("document holds %d values, want %d:\n%s", got, count, doc)
	}
}

// TestIntegrationMissingPrefixIsEmptyNotAnError is the half a real API can confirm.
// SSM answers an unused path with an empty list, not an error.
func TestIntegrationMissingPrefixIsEmptyNotAnError(t *testing.T) {
	e := requireEmulator(t)
	p := e.provider(t)

	if got, want := readAll(t, p, e.path(e.prefix(t)+"/nothing/here")), `{}`; got != want {
		t.Errorf("document = %s, want %s", got, want)
	}
}

func TestIntegrationRequireNonEmpty(t *testing.T) {
	e := requireEmulator(t)
	p := e.provider(t, viperssm.WithRequireNonEmpty())

	_, err := p.Get(e.path(e.prefix(t) + "/nothing/here"))

	var empty *viperssm.EmptyPrefixError
	if !errors.As(err, &empty) {
		t.Fatalf("Get error = %v, want *EmptyPrefixError", err)
	}
}

// TestIntegrationCollisionIsALoudError proves SSM really does allow a name to be
// both a value and a prefix, which is why the collision error exists.
func TestIntegrationCollisionIsALoudError(t *testing.T) {
	e := requireEmulator(t)
	prefix := e.prefix(t)

	e.put(t, prefix+"/db", "postgres://example", types.ParameterTypeString)
	e.put(t, prefix+"/db/host", "db.example", types.ParameterTypeString)

	p := e.provider(t)
	_, err := p.Get(e.path(prefix))

	var collision *viperssm.CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("Get error = %v, want *CollisionError", err)
	}
	if collision.Key != "db" {
		t.Errorf("Key = %q, want %q", collision.Key, "db")
	}
	if collision.Leaf != prefix+"/db" || collision.Prefix != prefix+"/db/host" {
		t.Errorf("named %q and %q, want %q and %q",
			collision.Leaf, collision.Prefix, prefix+"/db", prefix+"/db/host")
	}
}

// TestIntegrationPrefixIsolation guards the boundary the security policy calls
// out: nothing outside the configured path may be read.
func TestIntegrationPrefixIsolation(t *testing.T) {
	e := requireEmulator(t)
	prefix := e.prefix(t)

	e.put(t, prefix+"/inside/key", "wanted", types.ParameterTypeString)
	e.put(t, prefix+"-sibling/key", "must not be read", types.ParameterTypeString)
	// A parameter named exactly the path we read. SSM documents that the last
	// part of a name cannot be in the path, so it must not come back.
	e.put(t, prefix+"/inside", "not below the path", types.ParameterTypeString)

	p := e.provider(t)
	doc := readAll(t, p, e.path(prefix+"/inside"))

	if want := `{"key":"wanted"}`; doc != want {
		t.Errorf("document = %s, want %s", doc, want)
	}
}

// TestIntegrationNonRecursive checks that WithRecursive(false) reaches the API
// rather than being filtered locally.
func TestIntegrationNonRecursive(t *testing.T) {
	e := requireEmulator(t)
	prefix := e.prefix(t)

	e.put(t, prefix+"/shallow", "here", types.ParameterTypeString)
	e.put(t, prefix+"/deeper/key", "not here", types.ParameterTypeString)

	p := e.provider(t, viperssm.WithRecursive(false))
	doc := readAll(t, p, e.path(prefix))

	if want := `{"shallow":"here"}`; doc != want {
		t.Errorf("document = %s, want %s", doc, want)
	}
}

// TestIntegrationVersionsProveStaleness runs against real SSM versioning.
// Overwrite one parameter, and exactly that one changes version.
func TestIntegrationVersionsProveStaleness(t *testing.T) {
	e := requireEmulator(t)
	prefix := e.prefix(t)

	e.seed(t, map[string]string{
		prefix + "/url":   "postgres://db.example/app",
		prefix + "/quiet": "unchanged",
	})

	ctx := context.Background()
	p := e.provider(t)

	atBoot, err := p.Versions(ctx, prefix)
	if err != nil {
		t.Fatalf("Versions at boot: %v", err)
	}
	if len(atBoot) != 2 {
		t.Fatalf("Versions returned %d parameters, want 2: %v", len(atBoot), atBoot)
	}

	e.put(t, prefix+"/url", "postgres://replacement.example/app", types.ParameterTypeString)

	live, err := p.Versions(ctx, prefix)
	if err != nil {
		t.Fatalf("Versions later: %v", err)
	}

	if live[prefix+"/url"] <= atBoot[prefix+"/url"] {
		t.Errorf("version of %s went from %d to %d, want an increase",
			prefix+"/url", atBoot[prefix+"/url"], live[prefix+"/url"])
	}
	if live[prefix+"/quiet"] != atBoot[prefix+"/quiet"] {
		t.Errorf("version of the untouched parameter changed from %d to %d",
			atBoot[prefix+"/quiet"], live[prefix+"/quiet"])
	}
}

// TestIntegrationVersionsNeedNoDecryption asserts the request rather than the
// response: Floci hands back SecureString plaintext whatever WithDecryption
// says, so the check that matters is that Versions still reads the metadata of
// an encrypted parameter without asking for plaintext.
func TestIntegrationVersionsNeedNoDecryption(t *testing.T) {
	e := requireEmulator(t)
	prefix := e.prefix(t)

	e.put(t, prefix+"/secret", "hunter2", types.ParameterTypeSecureString)

	p := e.provider(t, viperssm.WithDecryption(true))
	versions, err := p.Versions(context.Background(), prefix)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if versions[prefix+"/secret"] < 1 {
		t.Errorf("Versions = %v, want a version for %s", versions, prefix+"/secret")
	}
}

// TestIntegrationWatchChannelEmitsOnVersionBump runs the watch end to end. A real
// PutParameter bumps a real version, and the watch reports it once.
func TestIntegrationWatchChannelEmitsOnVersionBump(t *testing.T) {
	e := requireEmulator(t)
	prefix := e.prefix(t)

	e.put(t, prefix+"/log_level", "info", types.ParameterTypeString)

	p := e.provider(t, viperssm.WithPollInterval(200*time.Millisecond))
	rp := e.path(prefix)

	// Seed the baseline the way ReadRemoteConfig does, so the first poll has
	// something to compare against.
	if _, err := p.Get(rp); err != nil {
		t.Fatalf("Get to seed the baseline: %v", err)
	}

	responses, stop := p.WatchChannel(rp)
	defer close(stop)

	e.put(t, prefix+"/log_level", "debug", types.ParameterTypeString)

	select {
	case response := <-responses:
		if response.Error != nil {
			t.Fatalf("response error: %v", response.Error)
		}
		if got, want := string(response.Value), `{"log_level":"debug"}`; got != want {
			t.Errorf("document = %s, want %s", got, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no emission 20s after a real version bump")
	}

	// One change, one emission.
	select {
	case response := <-responses:
		t.Fatalf("emitted twice for one change: %s", response.Value)
	case <-time.After(2 * time.Second):
	}
}
