// Package viperssm makes AWS SSM Parameter Store a first-class Viper remote
// provider. Reading configuration from it then also gets change detection and
// per-parameter versions.
//
// Viper reads remote configuration from etcd, Consul, Firestore and NATS.
// Parameter Store is not on that list. So teams read the parameters themselves
// and hand Viper a document. That costs two things:
// viper.WatchRemoteConfig stops working, and SSM's per-parameter version is
// thrown away. A running process then cannot prove its configuration is current.
//
// # Getting started
//
//	import (
//		"github.com/spf13/viper"
//		viperssm "github.com/matelang/viper-ssm"
//	)
//
//	if err := viperssm.Install(); err != nil {
//		return err
//	}
//
//	v := viper.New()
//	v.SetConfigType("json") // REQUIRED. See below.
//	if err := v.AddRemoteProvider("ssm", "eu-central-1", "/myapp/prod"); err != nil {
//		return err
//	}
//	if err := v.ReadRemoteConfig(); err != nil {
//		return err
//	}
//
//	v.GetString("database.url") // from /myapp/prod/database/url
//
// # SetConfigType("json") is not optional
//
// Viper parses a remote document with the configured config type. This provider
// returns JSON. Leave the line out and Viper gives you an empty configuration and
// no error. It is the likeliest way to misuse this package.
//
// # The endpoint is the region
//
// viper.AddRemoteProvider takes a provider, an endpoint and a path. Viper's
// RemoteProvider carries four strings and nothing else. So this package reads the
// endpoint as the AWS region, and the path as the parameter path. An endpoint
// that contains "://" is a URL, and this package uses it as the SSM endpoint
// instead. A VPC endpoint or a local emulator needs that.
//
// Versions takes a parameter path and no endpoint. WithEndpoint sets one for
// every provider that does not carry its own. A URL passed to WithRegion is an
// error that names the right option, rather than an "invalid input region" from
// deep inside the AWS SDK.
//
// The endpoint cannot be empty. Viper drops a provider whose endpoint is empty,
// and still returns nil. ReadRemoteConfig then reports "No Remote Providers".
// Pass a region, or use New and Get directly. Set anything richer once, through
// the Option functions: a timeout, a custom client, the poll interval.
//
// # Keys
//
// Strip the parameter path, then turn "/" into nesting. Under path /myapp/prod,
// the parameter /myapp/prod/database/url becomes the key database.url.
//
// Two layouts are errors, not a silent overwrite, and both name the two
// parameters involved. A key that is both a value and a path is a
// [CollisionError]: /myapp/prod/db next to /myapp/prod/db/host. Two parameters
// that map to one key are a [DuplicateKeyError], because SSM parameter names are
// case-sensitive and Viper keys are not.
//
// String and SecureString parameters become JSON strings. Viper's GetInt and
// GetBool convert from there. A StringList becomes a JSON array, split on commas
// as SSM defines it.
//
// # Proving configuration is current
//
// Versions returns the SSM version of every parameter under a path. Record it at
// boot, call it again later, and compare. SSM versions rise by one per edit, so a
// difference is an exact staleness verdict. It costs one call per page. It reads,
// holds and compares no secret, because Versions never asks SSM to decrypt.
//
// # Installing is additive
//
// Viper holds exactly one remote factory in viper.RemoteConfig. Install keeps the
// factory already there, and hands it every provider string this one does not
// own. So a program that blank-imports github.com/spf13/viper/remote keeps etcd,
// Consul, Firestore and NATS working. Import that package before Install, not
// after.
//
// # Values never reach logs
//
// Parameter names, types and versions appear in log lines and error messages.
// Values do not, anywhere. A test proves it against a sentinel.
package viperssm
