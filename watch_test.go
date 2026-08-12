package viperssm

import (
	"errors"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// watchInterval keeps the watch tests quick. Production defaults to
// DefaultPollInterval.
const watchInterval = 2 * time.Millisecond

// quietPeriod is how long a test waits to conclude that nothing was emitted. It
// is many poll intervals, so a missed emission is a failure rather than a race.
const quietPeriod = 100 * time.Millisecond

// TestWatchChannelIgnoresValueChangesAtTheSameVersion pins the rule that version
// decides change. So no secret value is ever held to compare.
func TestWatchChannelIgnoresValueChangesAtTheSameVersion(t *testing.T) {
	fake := &fakeSSM{}
	fake.set(strParam("/app/db/host", "first.example", 1))
	p := newProvider(t, fake, WithPollInterval(watchInterval))
	rp := ssmProvider("/app")

	responses, stop := p.WatchChannel(rp)
	defer close(stop)

	// A different value at the same version is not a change.
	fake.set(strParam("/app/db/host", "second.example", 1))
	select {
	case response := <-responses:
		t.Fatalf("emitted on an unchanged version: %s", response.Value)
	case <-time.After(quietPeriod):
	}

	// A version bump is.
	fake.set(strParam("/app/db/host", "third.example", 2))
	select {
	case response := <-responses:
		if response.Error != nil {
			t.Fatalf("response error: %v", response.Error)
		}
		if got, want := string(response.Value), `{"db":{"host":"third.example"}}`; got != want {
			t.Errorf("document = %s, want %s", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no emission after a version bump")
	}

	// Exactly one emission, so a re-read is not a re-notify.
	select {
	case response := <-responses:
		t.Fatalf("emitted twice for one change: %s", response.Value)
	case <-time.After(quietPeriod):
	}
}

func TestWatchChannelReportsAddedAndRemovedParameters(t *testing.T) {
	fake := &fakeSSM{}
	fake.set(strParam("/app/db/host", "example", 1))
	p := newProvider(t, fake, WithPollInterval(watchInterval))
	rp := ssmProvider("/app")

	responses, stop := startWatch(t, p, rp)
	defer close(stop)

	fake.set(
		strParam("/app/db/host", "example", 1),
		strParam("/app/db/port", "5432", 1),
	)
	response := awaitResponse(t, responses)
	if got, want := string(response.Value), `{"db":{"host":"example","port":"5432"}}`; got != want {
		t.Errorf("after an addition, document = %s, want %s", got, want)
	}

	fake.set(strParam("/app/db/host", "example", 1))
	response = awaitResponse(t, responses)
	if got, want := string(response.Value), `{"db":{"host":"example"}}`; got != want {
		t.Errorf("after a removal, document = %s, want %s", got, want)
	}
}

// TestWatchChannelSeedsItsBaselineFromAnEarlierGet keeps the first poll quiet
// when nothing has changed since the configuration was read.
func TestWatchChannelSeedsItsBaselineFromAnEarlierGet(t *testing.T) {
	fake := &fakeSSM{}
	fake.set(strParam("/app/db/host", "example", 1))
	p := newProvider(t, fake, WithPollInterval(watchInterval))
	rp := ssmProvider("/app")

	if _, err := p.Get(rp); err != nil {
		t.Fatalf("Get: %v", err)
	}
	callsAfterGet := fake.callCount()

	responses, stop := p.WatchChannel(rp)
	defer close(stop)

	select {
	case response := <-responses:
		t.Fatalf("emitted with nothing changed since Get: %s", response.Value)
	case <-time.After(quietPeriod):
	}

	if fake.callCount() <= callsAfterGet {
		t.Error("the watch never polled")
	}
}

// TestWatchChannelDeliversReadErrors keeps a failing read visible instead of
// silently freezing the configuration.
func TestWatchChannelDeliversReadErrors(t *testing.T) {
	throttled := errors.New("ThrottlingException: Rate exceeded")
	fake := &fakeSSM{}
	fake.set(strParam("/app/db/host", "example", 1))
	logger, logs := captureLogs()
	p := newProvider(t, fake, WithPollInterval(watchInterval), WithLogger(logger))

	responses, stop := startWatch(t, p, ssmProvider("/app"))
	defer close(stop)

	fake.setErr(throttled)
	response := awaitResponse(t, responses)
	if !errors.Is(response.Error, throttled) {
		t.Fatalf("response error = %v, want it to wrap the AWS error", response.Error)
	}

	// A recovery still reports the current document.
	fake.set(strParam("/app/db/host", "example", 2))
	response = awaitResponse(t, responses)
	if response.Error != nil {
		t.Fatalf("response error after recovery: %v", response.Error)
	}
	if got, want := string(response.Value), `{"db":{"host":"example"}}`; got != want {
		t.Errorf("document = %s, want %s", got, want)
	}
	if logs.Len() == 0 {
		t.Error("a failed poll logged nothing")
	}
}

// TestWatchChannelStopEndsItsGoroutines proves the teardown. The stop channel is
// the only signal Viper offers, so it has to be enough.
func TestWatchChannelStopEndsItsGoroutines(t *testing.T) {
	const frame = "(*Provider).poll"

	fake := &fakeSSM{}
	fake.set(strParam("/app/db/host", "example", 1))
	p := newProvider(t, fake, WithPollInterval(watchInterval))

	baseline := goroutinesMatching(frame)

	responses, stop := p.WatchChannel(ssmProvider("/app"))
	waitFor(t, time.Second, "the watch goroutines to start", func() bool {
		return goroutinesMatching(frame) > baseline
	})

	close(stop)
	waitFor(t, 2*time.Second, "the watch goroutines to end", func() bool {
		return goroutinesMatching(frame) == baseline
	})

	// Nothing should have been closed: Viper's receiver dereferences whatever
	// arrives, so a closed channel would hand it a nil pointer forever.
	select {
	case response, open := <-responses:
		if !open {
			t.Error("the response channel was closed; Viper's receiver would nil-panic on it")
		} else if response == nil {
			t.Error("the response channel delivered nil")
		}
	default:
	}
}

// TestWatchChannelStopBySend covers the other way Viper's own providers signal
// stop: a send rather than a close.
func TestWatchChannelStopBySend(t *testing.T) {
	const frame = "(*Provider).poll"

	fake := &fakeSSM{}
	fake.set(strParam("/app/db/host", "example", 1))
	p := newProvider(t, fake, WithPollInterval(watchInterval))

	baseline := goroutinesMatching(frame)
	_, stop := p.WatchChannel(ssmProvider("/app"))
	waitFor(t, time.Second, "the watch goroutines to start", func() bool {
		return goroutinesMatching(frame) > baseline
	})

	stop <- true
	waitFor(t, 2*time.Second, "the watch goroutines to end", func() bool {
		return goroutinesMatching(frame) == baseline
	})
}

// TestWatchRemoteConfigOnChannelThroughViper proves the Viper wiring works.
//
// It deliberately does not read a configuration value afterwards. Viper's
// watchKeyValueConfigOnChannel unmarshals into v.kvstore from its own goroutine
// with no synchronisation, so reading the same map from the test goroutine
// reports a data race in Viper rather than a defect here. The version-based
// emission itself is asserted directly against WatchChannel above.
func TestWatchRemoteConfigOnChannelThroughViper(t *testing.T) {
	isolateViper(t)
	viper.RemoteConfig = nil
	viper.SupportedRemoteProviders = []string{}

	fake := &fakeSSM{}
	fake.set(strParam("/myapp/prod/log_level", "info", 1))
	if err := Install(WithClient(fake), WithPollInterval(watchInterval)); err != nil {
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
	if err := v.WatchRemoteConfigOnChannel(); err != nil {
		t.Fatalf("WatchRemoteConfigOnChannel: %v", err)
	}

	before := fake.callCount()
	waitFor(t, 2*time.Second, "Viper's watch to poll SSM", func() bool {
		return fake.callCount() > before
	})
}

func awaitResponse(t *testing.T, responses <-chan *viper.RemoteResponse) *viper.RemoteResponse {
	t.Helper()
	select {
	case response := <-responses:
		return response
	case <-time.After(2 * time.Second):
		t.Fatal("no response within 2s")
		return nil
	}
}
