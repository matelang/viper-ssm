package viperssm

import (
	"context"
	"maps"
	"time"

	"github.com/spf13/viper"
)

// WatchChannel polls the parameter path, and sends a *viper.RemoteResponse each
// time something changes. Viper calls it from WatchRemoteConfigOnChannel.
//
// SSM parameter version decides change, never a comparison of values. A version
// rises by one per edit. So an edit that produces the same value still counts, and
// no secret is ever held to diff.
//
// The returned stop channel ends the poll. Send a value on it, or close it, and
// the goroutine returns. Note that Viper's WatchRemoteConfigOnChannel drops this
// channel, so a watch started through Viper runs for the life of the process. Call
// this method directly to keep a handle on it.
//
// Any provider string other than ProviderName goes to the factory Install kept.
func (p *Provider) WatchChannel(rp viper.RemoteProvider) (<-chan *viper.RemoteResponse, chan bool) {
	if rp.Provider() != ProviderName {
		if next := p.delegate(); next != nil {
			return next.WatchChannel(rp)
		}
		// The signature has no room for an error, so deliver it on the channel.
		// Buffer it, so nothing blocks when nobody reads.
		responses := make(chan *viper.RemoteResponse, 1)
		responses <- &viper.RemoteResponse{Error: &UnhandledProviderError{Provider: rp.Provider()}}
		return responses, make(chan bool, 1)
	}

	responses := make(chan *viper.RemoteResponse)
	stop := make(chan bool)
	go p.poll(rp, responses, stop)
	return responses, stop
}

// poll is the watch loop. It never closes the response channel. Viper's receiver
// reads a *viper.RemoteResponse and dereferences it without checking, so a closed
// channel would hand it a nil pointer forever.
func (p *Provider) poll(rp viper.RemoteProvider, responses chan<- *viper.RemoteResponse, stop <-chan bool) {
	// Viper gives us a stop channel and no context. Derive one from the channel,
	// so stopping also cancels a read already in flight.
	//
	// Viper's own providers stop with either a send or a close, so accept both.
	// Exactly one goroutine receives from stop, and everything else watches the
	// context. With two receivers, a send would reach one of them, and the other
	// would never learn the watch had stopped.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	send := func(response *viper.RemoteResponse) bool {
		select {
		case responses <- response:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Seed the baseline, so the first poll reports a change only if there was one.
	// An earlier Get on this path already recorded versions. Otherwise read them
	// now.
	baseline := p.knownVersions(rp)
	if baseline == nil {
		versions, _, err := p.load(ctx, rp)
		if err != nil {
			// Leave the baseline unset. The first successful poll then reports
			// a change. That is the safe direction to be wrong in.
			p.log().Error("SSM watch could not read its baseline",
				"prefix", rp.Path(), "error", err)
		} else {
			baseline = versions
		}
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		versions, doc, err := p.load(ctx, rp)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.log().Error("SSM watch failed to read parameters",
				"prefix", rp.Path(), "error", err)
			if !send(&viper.RemoteResponse{Error: err}) {
				return
			}
			continue
		}

		if baseline != nil && maps.Equal(baseline, versions) {
			continue
		}
		baseline = versions

		p.log().Info("SSM parameters changed", "prefix", rp.Path(), "count", len(versions))
		if !send(&viper.RemoteResponse{Value: doc}) {
			return
		}
	}
}
