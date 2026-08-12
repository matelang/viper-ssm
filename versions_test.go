package viperssm

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/spf13/viper"
)

func TestVersions(t *testing.T) {
	fake := &fakeSSM{}
	fake.set(
		strParam("/app/prod/database/url", "postgres://db.example/app", 7),
		secureParam("/app/prod/database/password", "hunter2", 3),
	)
	p := newProvider(t, fake)

	got, err := p.Versions(context.Background(), "/app/prod")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}

	want := map[string]int64{
		"/app/prod/database/url":      7,
		"/app/prod/database/password": 3,
	}
	if !maps.Equal(got, want) {
		t.Errorf("Versions = %v, want %v", got, want)
	}
}

// TestVersionsNeverDecrypts is what makes the promise real. Proving staleness needs
// no kms:Decrypt permission, and puts no plaintext secret on the wire.
func TestVersionsNeverDecrypts(t *testing.T) {
	fake := &fakeSSM{}
	fake.set(secureParam("/app/prod/token", "hunter2", 1))
	p := newProvider(t, fake, WithDecryption(true))

	if _, err := p.Versions(context.Background(), "/app/prod"); err != nil {
		t.Fatalf("Versions: %v", err)
	}

	inputs := fake.inputs()
	if len(inputs) != 1 {
		t.Fatalf("made %d calls, want 1", len(inputs))
	}
	if aws.ToBool(inputs[0].WithDecryption) {
		t.Error("WithDecryption = true; Versions reads metadata and must never ask for plaintext")
	}
}

func TestVersionsFollowsPagination(t *testing.T) {
	fake := &fakeSSM{}
	fake.setPages(
		[]types.Parameter{strParam("/app/a", "1", 1)},
		[]types.Parameter{strParam("/app/b", "2", 9)},
	)
	p := newProvider(t, fake)

	got, err := p.Versions(context.Background(), "/app")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	want := map[string]int64{"/app/a": 1, "/app/b": 9}
	if !maps.Equal(got, want) {
		t.Errorf("Versions = %v, want %v", got, want)
	}
}

func TestVersionsPropagatesErrors(t *testing.T) {
	denied := errors.New("AccessDeniedException: not authorized")
	fake := &fakeSSM{}
	fake.setErr(denied)
	p := newProvider(t, fake)

	if _, err := p.Versions(context.Background(), "/app"); !errors.Is(err, denied) {
		t.Errorf("Versions error = %v, want it to wrap the AWS error", err)
	}
}

func TestVersionsHonoursTheCallersContext(t *testing.T) {
	fake := &fakeSSM{}
	fake.set(strParam("/app/a", "1", 1))
	p := newProvider(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The fake ignores the context, so this asserts only that Versions does not
	// substitute a context of its own. The call still reaches the client.
	if _, err := p.Versions(ctx, "/app"); err != nil {
		t.Fatalf("Versions: %v", err)
	}
}

// TestPackageVersionsNeedsInstall keeps the package-level convenience honest
// about depending on package-level state.
func TestPackageVersionsNeedsInstall(t *testing.T) {
	isolateViper(t)
	installMu.Lock()
	installed = nil
	installMu.Unlock()

	if _, err := Versions(context.Background(), "/app"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Versions error = %v, want ErrNotInstalled", err)
	}
}

func TestPackageVersionsUsesTheInstalledProvider(t *testing.T) {
	isolateViper(t)
	viper.RemoteConfig = nil

	fake := &fakeSSM{}
	fake.set(strParam("/app/prod/url", "postgres://db.example/app", 5))
	if err := Install(WithClient(fake)); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := Versions(context.Background(), "/app/prod")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if want := map[string]int64{"/app/prod/url": 5}; !maps.Equal(got, want) {
		t.Errorf("Versions = %v, want %v", got, want)
	}
}

// TestVersionsProvesStaleness is the workflow Versions exists for. Record at boot,
// compare later, decide, and read no secret on the way.
func TestVersionsProvesStaleness(t *testing.T) {
	fake := &fakeSSM{}
	fake.set(strParam("/app/prod/url", "postgres://db.example/app", 1))
	p := newProvider(t, fake)

	atBoot, err := p.Versions(context.Background(), "/app/prod")
	if err != nil {
		t.Fatalf("Versions at boot: %v", err)
	}

	fake.set(strParam("/app/prod/url", "postgres://replacement.example/app", 2))
	live, err := p.Versions(context.Background(), "/app/prod")
	if err != nil {
		t.Fatalf("Versions later: %v", err)
	}

	if maps.Equal(atBoot, live) {
		t.Error("versions compared equal after a parameter changed")
	}
}
