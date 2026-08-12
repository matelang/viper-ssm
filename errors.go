package viperssm

import (
	"errors"
	"fmt"
)

// ErrNotInstalled reports that a package-level call needs Install to run first.
// Build a *Provider with New to avoid the package-level state.
var ErrNotInstalled = errors.New("viperssm: not installed; call viperssm.Install first")

// ErrEmptyPath reports an empty parameter path. SSM paths start with a slash.
// Reading an empty path as "/" would pull every parameter in the account, so it
// is an error instead.
var ErrEmptyPath = errors.New(`viperssm: empty parameter path; pass a path such as "/myapp/prod", or "/" to read every parameter`)

// CollisionError reports two parameters that cannot share one configuration
// document. One holds a value at a key. The other needs that same key to be a
// nested object.
//
// It names both parameters, so the collision is fixable without guessing. It
// never names a value.
type CollisionError struct {
	// Key is the configuration key both parameters claim. It is in Viper's
	// dotted, lowercased form.
	Key string
	// Leaf is the parameter that holds a value at Key.
	Leaf string
	// Prefix is the parameter that needs Key to be a nested object.
	Prefix string
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf("viperssm: parameter collision at key %q: %s holds a value, but %s needs %q to be a nested object",
		e.Key, e.Leaf, e.Prefix, e.Key)
}

// DuplicateKeyError reports two parameters that map to one configuration key.
//
// SSM parameter names are case-sensitive. Viper keys are not. So /app/DB/host and
// /app/db/host are two parameters and one key. Viper would keep whichever arrived
// last. This package refuses instead.
type DuplicateKeyError struct {
	// Key is the configuration key, in Viper's dotted and lowercased form.
	Key string
	// First and Second are the two parameter names that map to Key.
	First  string
	Second string
}

func (e *DuplicateKeyError) Error() string {
	return fmt.Sprintf("viperssm: parameters %s and %s both map to configuration key %q; Viper keys are case-insensitive",
		e.First, e.Second, e.Key)
}

// InvalidNameError reports a parameter name this package cannot turn into a
// configuration key.
type InvalidNameError struct {
	// Name is the parameter name as SSM returned it.
	Name string
	// Prefix is the path the parameters were read under.
	Prefix string
	// Reason says what is wrong with Name.
	Reason string
}

func (e *InvalidNameError) Error() string {
	return fmt.Sprintf("viperssm: parameter %s under %q: %s", e.Name, e.Prefix, e.Reason)
}

// EmptyPrefixError reports that a parameter path holds nothing. Only a provider
// built with WithRequireNonEmpty returns it. By default an empty path is an empty
// document and no error.
type EmptyPrefixError struct {
	// Prefix is the path that held no parameters.
	Prefix string
}

func (e *EmptyPrefixError) Error() string {
	return fmt.Sprintf("viperssm: no parameters found under %q", e.Prefix)
}

// UnhandledProviderError reports a remote provider string that belongs to neither
// this package nor any factory it delegates to.
//
// Viper keeps exactly one remote factory. Install keeps whatever is already
// registered and delegates to it. So this error means nothing was registered when
// Install ran.
type UnhandledProviderError struct {
	// Provider is the unhandled provider string.
	Provider string
}

func (e *UnhandledProviderError) Error() string {
	return fmt.Sprintf("viperssm: remote provider %q is not %q, and no other factory was installed when viperssm.Install ran; "+
		"blank-import github.com/spf13/viper/remote before Install to keep Viper's built-in providers working",
		e.Provider, ProviderName)
}
