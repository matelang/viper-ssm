package viperssm

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// maxResultsPerPage is the largest page GetParametersByPath allows.
const maxResultsPerPage = 10

// fetch reads every parameter under path, and follows pagination to the end. It
// returns the normalised path with the parameters, because the caller needs the
// same normalisation to strip it off again.
//
// Throttling and retry are the AWS SDK's job. GetParametersByPath is rate-limited,
// and the standard retryer applies. This package adds no retry of its own.
func (p *Provider) fetch(ctx context.Context, endpoint, path string, decrypt bool) (string, []types.Parameter, error) {
	prefix, err := normalizePath(path)
	if err != nil {
		return "", nil, err
	}

	client, err := p.clientFor(ctx, endpoint)
	if err != nil {
		return "", nil, err
	}

	in := &ssm.GetParametersByPathInput{
		Path:           aws.String(prefix),
		Recursive:      aws.Bool(p.recursive),
		WithDecryption: aws.Bool(decrypt),
		MaxResults:     aws.Int32(maxResultsPerPage),
	}

	var params []types.Parameter
	seen := make(map[string]struct{})
	for {
		// The request carries a path and three flags, never a value. So nothing
		// the AWS SDK can put in an error came from a parameter value.
		out, err := client.GetParametersByPath(ctx, in)
		if err != nil {
			return "", nil, fmt.Errorf("viperssm: read parameters under %q: %w", prefix, err)
		}
		params = append(params, out.Parameters...)

		token := aws.ToString(out.NextToken)
		if token == "" {
			return prefix, params, nil
		}
		if _, repeated := seen[token]; repeated {
			return "", nil, fmt.Errorf("viperssm: read parameters under %q: pagination token repeated after %d parameters",
				prefix, len(params))
		}
		seen[token] = struct{}{}
		in.NextToken = aws.String(token)
	}
}

// isURL decides the region-or-endpoint question by the one marker a region name
// can never contain.
func isURL(s string) bool {
	return strings.Contains(s, "://")
}

// clientFor returns the SSM client for one remote provider's endpoint.
//
// An endpoint that contains "://" is a URL, and becomes the client's base
// endpoint. A VPC endpoint or a local emulator needs that. Anything else is a
// region name. An empty endpoint falls back to WithEndpoint and WithRegion. An
// empty region falls back to whatever the AWS SDK resolves on its own.
func (p *Provider) clientFor(ctx context.Context, endpoint string) (ParameterReader, error) {
	if p.client != nil {
		return p.client, nil
	}

	region, baseEndpoint := p.region, p.endpoint
	if endpoint != "" {
		if isURL(endpoint) {
			baseEndpoint = endpoint
		} else {
			region = endpoint
		}
	}

	// The key holds both halves. One client per region and endpoint pair, not one
	// per endpoint. Otherwise a second region would get the first one's client.
	key := region + "\x00" + baseEndpoint

	p.mu.RLock()
	cached := p.clients[key]
	p.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	var loadOpts []func(*awsconfig.LoadOptions) error
	if region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("viperssm: load AWS configuration: %w", err)
	}

	var ssmOpts []func(*ssm.Options)
	if baseEndpoint != "" {
		ssmOpts = append(ssmOpts, func(o *ssm.Options) { o.BaseEndpoint = aws.String(baseEndpoint) })
	}
	client := ParameterReader(ssm.NewFromConfig(cfg, ssmOpts...))

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients == nil {
		p.clients = make(map[string]ParameterReader)
	}
	if raced := p.clients[key]; raced != nil {
		return raced, nil
	}
	p.clients[key] = client
	return client, nil
}

// normalizePath puts a parameter path in the one form the rest of the package
// expects: a leading slash, and no trailing slash unless the path is the root.
func normalizePath(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", ErrEmptyPath
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	return p, nil
}

// versionsOf maps parameter name to SSM version.
func versionsOf(params []types.Parameter) map[string]int64 {
	out := make(map[string]int64, len(params))
	for _, param := range params {
		if param.Name == nil {
			continue
		}
		out[*param.Name] = param.Version
	}
	return out
}
