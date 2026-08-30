package cli

import (
	"context"

	"github.com/m-mizutani/octify/pkg/usecase"
	ucli "github.com/urfave/cli/v3"
)

// ConfigForTest parses argv through the same flag definitions Run uses and
// returns the configuration the command would hand to the use case layer.
func ConfigForTest(ctx context.Context, argv []string) (usecase.Config, error) {
	var opt options
	var got usecase.Config

	cmd := &ucli.Command{
		Name:  "octify",
		Flags: opt.flags(),
		Action: func(context.Context, *ucli.Command) error {
			got = opt.usecaseConfig()
			return nil
		},
	}
	if err := cmd.Run(ctx, argv); err != nil {
		return usecase.Config{}, err
	}
	return got, nil
}
