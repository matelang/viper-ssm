package viperssm_test

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/spf13/viper"

	viperssm "github.com/matelang/viper-ssm"
)

// staticStore is a stand-in for Parameter Store, so that these examples run
// without an AWS account. Real code omits WithClient and lets the AWS SDK build
// the client from the standard credential chain.
type staticStore struct {
	params []types.Parameter
}

func (s *staticStore) GetParametersByPath(_ context.Context, _ *ssm.GetParametersByPathInput,
	_ ...func(*ssm.Options),
) (*ssm.GetParametersByPathOutput, error) {
	return &ssm.GetParametersByPathOutput{Parameters: s.params}, nil
}

func parameter(name, value string, version int64) types.Parameter {
	return types.Parameter{
		Name:    aws.String(name),
		Value:   aws.String(value),
		Type:    types.ParameterTypeString,
		Version: version,
	}
}

// Example reads configuration from Parameter Store through Viper.
func Example() {
	store := &staticStore{params: []types.Parameter{
		parameter("/myapp/prod/database/url", "postgres://db.example/app", 3),
		parameter("/myapp/prod/http/port", "8080", 1),
	}}

	// Real code: viperssm.Install(viperssm.WithRegion("eu-central-1")).
	if err := viperssm.Install(viperssm.WithClient(store)); err != nil {
		log.Fatal(err)
	}

	v := viper.New()
	v.SetConfigType("json") // REQUIRED: this provider returns a JSON document.
	if err := v.AddRemoteProvider("ssm", "eu-central-1", "/myapp/prod"); err != nil {
		log.Fatal(err)
	}
	if err := v.ReadRemoteConfig(); err != nil {
		log.Fatal(err)
	}

	fmt.Println(v.GetString("database.url"))
	fmt.Println(v.GetInt("http.port"))
	// Output:
	// postgres://db.example/app
	// 8080
}

// ExampleVersions proves a running process is still on current configuration,
// without reading, holding or comparing any secret value.
func ExampleVersions() {
	store := &staticStore{params: []types.Parameter{
		parameter("/myapp/prod/database/url", "postgres://db.example/app", 3),
	}}
	if err := viperssm.Install(viperssm.WithClient(store)); err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	atBoot, err := viperssm.Versions(ctx, "/myapp/prod")
	if err != nil {
		log.Fatal(err)
	}

	// Somebody edits the parameter. SSM bumps its version.
	store.params = []types.Parameter{
		parameter("/myapp/prod/database/url", "postgres://replacement.example/app", 4),
	}

	live, err := viperssm.Versions(ctx, "/myapp/prod")
	if err != nil {
		log.Fatal(err)
	}

	for name, booted := range atBoot {
		if live[name] != booted {
			fmt.Printf("%s is stale: running version %d, current version %d\n", name, booted, live[name])
		}
	}
	// Output:
	// /myapp/prod/database/url is stale: running version 3, current version 4
}

// ExampleProvider_WatchChannel watches a parameter path directly, which is the
// way to keep a handle on the watch. viper.WatchRemoteConfigOnChannel discards
// the stop channel, so a watch started through Viper runs for the life of the
// process.
func ExampleProvider_WatchChannel() {
	p, err := viperssm.New(
		viperssm.WithRegion("eu-central-1"),
		viperssm.WithPollInterval(30*time.Second),
	)
	if err != nil {
		log.Fatal(err)
	}

	v := viper.New()
	v.SetConfigType("json")
	if err := v.AddRemoteProvider("ssm", "eu-central-1", "/myapp/prod"); err != nil {
		log.Fatal(err)
	}

	responses, stop := p.WatchChannel(ssmPath{region: "eu-central-1", path: "/myapp/prod"})
	defer close(stop)

	for response := range responses {
		if response.Error != nil {
			log.Printf("SSM watch: %v", response.Error)
			continue
		}
		if err := v.ReadConfig(bytes.NewReader(response.Value)); err != nil {
			log.Printf("reread configuration: %v", err)
		}
	}
}

// ssmPath is a viper.RemoteProvider written by hand, which is what calling
// WatchChannel or Get directly needs. Viper's own AddRemoteProvider builds an
// equivalent one from its three arguments.
type ssmPath struct {
	region string
	path   string
}

func (s ssmPath) Provider() string      { return "ssm" }
func (s ssmPath) Endpoint() string      { return s.region }
func (s ssmPath) Path() string          { return s.path }
func (s ssmPath) SecretKeyring() string { return "" }
