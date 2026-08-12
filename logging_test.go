package viperssm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// sentinel is a recognisable parameter value. It must never appear in a log line
// or an error message, only in the configuration document itself.
const sentinel = "S3NTINEL-never-log-this-value"

// TestValuesNeverReachLogsOrErrors is the rule this package will not bend.
// Parameter names, types and versions are fair game. Values are not, anywhere.
func TestValuesNeverReachLogsOrErrors(t *testing.T) {
	tests := []struct {
		name string
		// run exercises one code path with the sentinel as a parameter value
		// and returns whatever error it produced.
		run func(t *testing.T, p *Provider, fake *fakeSSM) error
	}{
		{
			name: "a successful read",
			run: func(t *testing.T, p *Provider, fake *fakeSSM) error {
				fake.set(secureParam("/app/prod/database/password", sentinel, 1))
				// The document does carry the value; that is its job. Read it
				// to be sure the sentinel really flowed through this path.
				if doc := readDocument(t, p, ssmProvider("/app/prod")); !strings.Contains(doc, sentinel) {
					t.Fatalf("this test proves nothing: the sentinel never reached the document (%s)", doc)
				}
				return nil
			},
		},
		{
			name: "a path collision",
			run: func(_ *testing.T, p *Provider, fake *fakeSSM) error {
				fake.set(
					secureParam("/app/prod/db", sentinel, 1),
					strParam("/app/prod/db/host", "db.example", 1),
				)
				_, err := p.Get(ssmProvider("/app/prod"))
				return err
			},
		},
		{
			name: "two parameters on one key",
			run: func(_ *testing.T, p *Provider, fake *fakeSSM) error {
				fake.set(
					secureParam("/app/prod/TOKEN", sentinel, 1),
					secureParam("/app/prod/token", sentinel, 1),
				)
				_, err := p.Get(ssmProvider("/app/prod"))
				return err
			},
		},
		{
			name: "an unusable parameter name",
			run: func(_ *testing.T, p *Provider, fake *fakeSSM) error {
				fake.set(secureParam("/app/prod//token", sentinel, 1))
				_, err := p.Get(ssmProvider("/app/prod"))
				return err
			},
		},
		{
			name: "a parameter with no name",
			run: func(_ *testing.T, p *Provider, fake *fakeSSM) error {
				fake.set(namelessParam(sentinel))
				_, err := p.Get(ssmProvider("/app/prod"))
				return err
			},
		},
		{
			name: "a failed read",
			run: func(_ *testing.T, p *Provider, fake *fakeSSM) error {
				fake.setErr(errors.New("AccessDeniedException: not authorized"))
				_, err := p.Get(ssmProvider("/app/prod"))
				return err
			},
		},
		{
			name: "reading versions",
			run: func(_ *testing.T, p *Provider, fake *fakeSSM) error {
				fake.set(secureParam("/app/prod/token", sentinel, 4))
				_, err := p.Versions(context.Background(), "/app/prod")
				return err
			},
		},
		{
			name: "a watched change",
			run: func(t *testing.T, p *Provider, fake *fakeSSM) error {
				fake.set(secureParam("/app/prod/token", sentinel, 1))
				responses, stop := startWatch(t, p, ssmProvider("/app/prod"))
				defer close(stop)

				fake.set(secureParam("/app/prod/token", sentinel+"-rotated", 2))
				select {
				case response := <-responses:
					if response.Error != nil {
						return response.Error
					}
				case <-time.After(2 * time.Second):
					t.Fatal("no emission after a version bump")
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, logs := captureLogs()
			fake := &fakeSSM{}
			p := newProvider(t, fake,
				WithLogger(logger),
				WithPollInterval(2*time.Millisecond),
			)

			err := tt.run(t, p, fake)

			if got := logs.String(); strings.Contains(got, sentinel) {
				t.Errorf("a parameter value reached the log:\n%s", got)
			}
			if err != nil && strings.Contains(err.Error(), sentinel) {
				t.Errorf("a parameter value reached an error message:\n%s", err)
			}
		})
	}
}

// TestErrorsNameParametersNotValues checks the other half of the rule: the
// errors have to be actionable, which means naming the parameters involved.
func TestErrorsNameParametersNotValues(t *testing.T) {
	fake := &fakeSSM{}
	fake.set(
		secureParam("/app/prod/db", sentinel, 1),
		strParam("/app/prod/db/host", "db.example", 1),
	)
	p := newProvider(t, fake)

	_, err := p.Get(ssmProvider("/app/prod"))
	if err == nil {
		t.Fatal("Get returned no error on a collision")
	}
	for _, want := range []string{"/app/prod/db", "/app/prod/db/host", "db"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
